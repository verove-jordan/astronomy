package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// The hand-controller command line: diagnose the link, prove it, then leave it running all night.
//
// These live outside the device server on purpose. macOS serial opens take TIOCEXCL, so exactly one
// process may hold the port — which means the diagnosis and the endurance run cannot be endpoints on
// a server that is itself holding it. The trade is that `astrostack device` must be stopped first,
// and the error below says so rather than reporting a hardware fault.

func runMount(args []string) error {
	if len(args) == 0 {
		mountUsage()
		return fmt.Errorf("expected a subcommand")
	}
	switch args[0] {
	case "doctor":
		return runMountDoctor(args[1:])
	case "probe":
		return runMountProbe(args[1:])
	case "soak":
		return runMountSoak(args[1:])
	case "help", "--help", "-h":
		mountUsage()
		return nil
	default:
		mountUsage()
		return fmt.Errorf("unknown mount subcommand %q", args[0])
	}
}

func mountUsage() {
	fmt.Fprint(os.Stderr, `astrostack mount — Celestron hand-controller link

Usage:
  astrostack mount doctor [-probe]        why this Mac can (or cannot) see the hand controller
  astrostack mount probe  [-port PATH]    connect, identify the mount, and time 500 echoes
  astrostack mount soak   [flags]         run the overnight endurance test and write a report

Soak flags:
  -port PATH        serial device (default: the first that looks like a USB-serial adapter)
  -duration 8h      how long to run
  -motion none      none | nudge   (nudge = ±10" out and back every 10 min, plus an hourly
                                    jog-and-deadman check; never a GoTo, never a large slew)
  -report PATH      write the report here as well as to the terminal (.json alongside)
  -allow-reconnects 0
                    tolerate this many reconnects, for a deliberate unplug drill

Stop 'astrostack device' first: macOS gives the serial port to one process at a time.
`)
}

func runMountDoctor(args []string) error {
	fs := flag.NewFlagSet("mount doctor", flag.ContinueOnError)
	probe := fs.Bool("probe", false, "also open each candidate port and ask the mount to identify itself")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d := nexstar.Diagnose(ctx, *probe)
	fmt.Print(d.String())
	if !d.OK() {
		// A non-zero exit makes this usable as a pre-flight gate in a script, without anyone having to
		// parse the text.
		return fmt.Errorf("verdict: %s", d.Verdict)
	}
	return nil
}

func runMountProbe(args []string) error {
	fs := flag.NewFlagSet("mount probe", flag.ContinueOnError)
	port := fs.String("port", "", "serial device (default: the first likely USB-serial adapter)")
	echoes := fs.Int("echoes", 500, "how many back-to-back echoes to time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	m, err := connectMount(ctx, *port)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	st, err := m.State(ctx)
	if err != nil {
		return fmt.Errorf("read the mount's state: %w", err)
	}
	fmt.Printf("connected   %s  firmware %s  on %s\n", m.Model(), m.Firmware(), m.Path())
	fmt.Printf("position    RA %.4f°  Dec %.4f°  (J2000)\n", st.RADeg, st.DecDeg)
	fmt.Printf("state       aligned=%v  slewing=%v  tracking=%v (%s)  pier=%s\n",
		st.Aligned, st.Slewing, st.Tracking, st.TrackingRate, st.PierSide)
	if !st.Aligned {
		fmt.Println("note        the mount is not aligned, so GoTo will be refused — expected on a bench")
	}

	// The echo storm is the cheapest honest measure of the link: no mount logic, no motors, just the
	// round trip. Anything wrong with the cable, the bridge or the USB power shows up here first.
	fmt.Printf("\ntiming %d echoes...\n", *echoes)
	start := time.Now()
	before := m.Health()
	for i := 0; i < *echoes; i++ {
		if err := m.Ping(ctx); err != nil {
			return fmt.Errorf("echo %d of %d failed: %w", i+1, *echoes, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	h := m.Health()
	fmt.Printf("            %d echoes in %s — p50 %dms  p99 %dms  max %dms\n",
		*echoes, time.Since(start).Round(time.Millisecond), h.LatencyP50Ms, h.LatencyP99Ms, h.LatencyMaxMs)
	fmt.Printf("            errors %d  retries %d  resyncs %d  reconnects %d\n",
		h.Errors-before.Errors, h.Retries-before.Retries, h.Resyncs-before.Resyncs, h.Reconnects-before.Reconnects)
	if h.Errors > before.Errors || h.Resyncs > before.Resyncs {
		return fmt.Errorf("the link is not clean: a healthy hand controller answers 500 echoes with no errors and no resynchronisations")
	}
	return nil
}

func runMountSoak(args []string) error {
	fs := flag.NewFlagSet("mount soak", flag.ContinueOnError)
	port := fs.String("port", "", "serial device (default: the first likely USB-serial adapter)")
	duration := fs.Duration("duration", 8*time.Hour, "how long to run")
	motion := fs.String("motion", "none", "none | nudge")
	report := fs.String("report", "", "write the report to this path (a .json sibling is written too)")
	allowReconnects := fs.Int("allow-reconnects", 0, "tolerate this many reconnects (for an unplug drill)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Nothing else in this repo needs the Mac awake, but a sleeping Mac drops the USB bus, and an
	// eight-hour run that ends at the lid closing is not evidence of anything. caffeinate exits with
	// us, so there is no assertion left behind if the run is interrupted.
	if stop := keepAwake(); stop != nil {
		defer stop()
	}

	m, err := connectMount(ctx, *port)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	cfg := nexstar.SoakConfig{
		Duration:        *duration,
		AllowReconnects: *allowReconnects,
		Progress:        os.Stdout,
	}
	switch *motion {
	case "none":
	case "nudge":
		cfg.NudgeInterval, cfg.NudgeArcsec = 10*time.Minute, 10
		cfg.JogCheckInterval = time.Hour
	default:
		return fmt.Errorf("unknown -motion %q (use none or nudge)", *motion)
	}

	fmt.Printf("soaking %s on %s for %s (motion: %s)\n\n", m.Model(), m.Path(), *duration, *motion)
	r, err := nexstar.Soak(ctx, m, cfg)
	if err != nil {
		return err
	}
	r.JudgeReconnects(*allowReconnects)
	r.JudgeCoverage(cfg)

	fmt.Print("\n" + r.String())
	if *report != "" {
		if err := os.WriteFile(*report, []byte(r.String()), 0o644); err != nil {
			return fmt.Errorf("write the report: %w", err)
		}
		if b, err := r.JSON(); err == nil {
			_ = os.WriteFile(*report+".json", b, 0o644)
		}
		fmt.Printf("\nreport written to %s\n", *report)
	}
	if !r.Pass() {
		return fmt.Errorf("the soak did not pass")
	}
	return nil
}

// connectMount opens the hand controller, turning the two failures people actually hit into
// instructions rather than error codes.
func connectMount(ctx context.Context, port string) (*nexstar.Mount, error) {
	if port == "" {
		port = nexstar.DefaultPort()
	}
	if port == "" {
		d := nexstar.Diagnose(ctx, false)
		return nil, fmt.Errorf("no hand controller found\n\n%s", d.String())
	}
	m := nexstar.New(port, nil)
	if err := m.Connect(ctx); err != nil {
		d := nexstar.Diagnose(ctx, false)
		return nil, fmt.Errorf("could not connect on %s: %w\n\n%s", port, err, d.String())
	}
	return m, nil
}

// keepAwake stops the Mac idling to sleep for as long as this process runs. It is best-effort: a
// machine without caffeinate simply runs without the assertion rather than refusing to soak.
func keepAwake() func() {
	cmd := exec.Command("caffeinate", "-imsu", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil
	}
	return func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
}
