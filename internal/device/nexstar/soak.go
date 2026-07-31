package nexstar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The overnight endurance run.
//
// Everything else in this package proves a behaviour in milliseconds against a fake. This proves the
// thing that cannot be faked: that a real MacBook, a real Prolific bridge and a real hand controller
// stay in conversation for eight hours without a human in the room. It is the same code whether it
// is driven from the CLI or from the gated live test, because a soak that is not the thing you
// actually run at night proves nothing about the night.
//
// The run is deliberately dull. It polls at the cadence the UI polls, pings, re-reads the identity,
// and — only when asked — makes small reversible moves. What matters is the report: a night that
// ends badly must be diagnosable in the morning from counters and a latency histogram, not from
// "it stopped".

// SoakConfig describes an endurance run. The zero value is a sane read-only run.
type SoakConfig struct {
	// Duration is wall-clock; the whole point is to cover a night rather than a sample.
	Duration time.Duration
	// PollInterval is how often the full state is read. Defaults to what the UI does.
	PollInterval time.Duration
	// PingInterval is how often the randomised echo runs. It is the ONLY thing that can prove the
	// reply stream is still in step, so it is never disabled.
	PingInterval time.Duration
	// IdentityInterval is how often the firmware and model are re-read, to catch a hand controller
	// that rebooted behind our back.
	IdentityInterval time.Duration
	// NudgeInterval, when non-zero, moves the mount ±NudgeArcsec and back. Off by default: the
	// caller must decide the mount is safe to move.
	NudgeInterval time.Duration
	NudgeArcsec   float64
	// JogCheckInterval, when non-zero, starts a jog at the gentlest rate and then deliberately fails
	// to renew it, proving the deadman stops the axis. Rate 1 does not even override equatorial
	// tracking, so this is the smallest real motion the mount can make.
	JogCheckInterval time.Duration
	// AllowReconnects turns the deliberate unplug drill from a failure into a counted warning.
	AllowReconnects int
	// Progress receives one line per minute so an operator can see it is alive.
	Progress io.Writer
}

func (c *SoakConfig) applyDefaults() {
	if c.Duration <= 0 {
		c.Duration = time.Hour
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.PingInterval <= 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.IdentityInterval <= 0 {
		c.IdentityInterval = 5 * time.Minute
	}
	if c.NudgeInterval > 0 && c.NudgeArcsec == 0 {
		c.NudgeArcsec = 10
	}
}

// SoakReport is the morning-after evidence.
type SoakReport struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Model    string    `json:"model"`
	Firmware string    `json:"firmware"`
	Path     string    `json:"path"`

	Polls    int `json:"polls"`
	Pings    int `json:"pings"`
	Nudges   int `json:"nudges"`
	JogTests int `json:"jog_tests"`

	IdentityChecks     int `json:"identity_checks"`
	IdentityMismatches int `json:"identity_mismatches"`

	// PollFailures counts state reads that returned an error the link did not recover from within
	// the poll itself. They are the headline number: a soak with any of these did not pass.
	PollFailures int `json:"poll_failures"`
	PingFailures int `json:"ping_failures"`
	MoveFailures int `json:"move_failures"`

	Link LinkHealth `json:"link"`

	ResyncsLastHour uint64 `json:"resyncs_last_hour"`
	LatencyP50Ms    int64  `json:"latency_p50_ms"`
	LatencyP90Ms    int64  `json:"latency_p90_ms"`
	LatencyP99Ms    int64  `json:"latency_p99_ms"`
	LatencyMaxMs    int64  `json:"latency_max_ms"`
	LatencyMeanMs   int64  `json:"latency_mean_ms"`

	GoroutinesStart int    `json:"goroutines_start"`
	GoroutinesEnd   int    `json:"goroutines_end"`
	HeapStartBytes  uint64 `json:"heap_start_bytes"`
	HeapEndBytes    uint64 `json:"heap_end_bytes"`

	// Failures is one line per breached threshold. Empty means PASS, and nothing else does.
	Failures []string `json:"failures"`
	// Notes records things worth knowing that are not failures, such as a mount that was never
	// aligned — which is expected on a bench and would otherwise look like a missing test.
	Notes []string `json:"notes"`
}

func (r SoakReport) Pass() bool { return len(r.Failures) == 0 }

// Soak thresholds. Each one is arithmetic, not taste.
//
// At 9600 8N1 a byte takes 1.04 ms. A precise position query is one byte out and eighteen back, so
// about 20 ms of pure wire time; a full state read (position, slewing, aligned, tracking, pier) is
// about 32 ms. Anything far above that is the hand controller being starved, not the link being slow.
const (
	// soakP50CeilingMs is roughly three to six times the wire-plus-turnaround expectation.
	soakP50CeilingMs = 120
	// soakP99CeilingMs sits well under Celestron's documented 3.5 s, with room for the hand
	// controller to service its own keypad and display.
	soakP99CeilingMs = 800
	// soakMaxCeilingMs is the protocol ceiling itself: at or over it, the reply WAS a timeout.
	soakMaxCeilingMs = 3500
	// soakResyncsPerHour tolerates the occasional line-noise event on an unshielded cable near a dew
	// heater. More than this is systemic — a cable, a ground loop, or a hub that cannot feed the bus.
	soakResyncsPerHour = 2
	// soakGoroutineSlack allows for the watchdog resting between ticks.
	soakGoroutineSlack = 2
	// soakHeapGrowthBytes is generous for a run that only ever allocates small reply buffers.
	soakHeapGrowthBytes = 8 << 20
	// soakMinCommandFraction guards against a run that quietly stopped polling and still "passed".
	soakMinCommandFraction = 0.9
)

// Soak runs the endurance test and returns its report. It returns an error only when the run could
// not be performed at all; a run that completed and FAILED its thresholds is a report, not an error,
// because the numbers are the point.
func Soak(ctx context.Context, m *Mount, cfg SoakConfig) (SoakReport, error) {
	cfg.applyDefaults()
	if !m.Connected() {
		return SoakReport{}, fmt.Errorf("the mount is not connected")
	}

	start := time.Now()
	r := SoakReport{
		Started: start, Model: m.Model(), Firmware: m.Firmware(), Path: m.Path(),
		GoroutinesStart: runtime.NumGoroutine(), HeapStartBytes: heapInUse(),
	}
	if st, err := m.State(ctx); err == nil && !st.Aligned {
		r.Notes = append(r.Notes,
			"the mount reported itself unaligned for the whole run, so GoTo was never exercised — expected on a bench, not on sky")
	}

	deadline := start.Add(cfg.Duration)
	var (
		lastPing, lastIdentity, lastNudge, lastJog, lastProgress time.Time
		resyncsAtHourStart                                       uint64
		hourStart                                                = start
	)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			r.Notes = append(r.Notes, "the run was cancelled before its full duration")
			return r.finish(m, start, hourStart, resyncsAtHourStart), nil
		case <-time.After(cfg.PollInterval):
		}

		if _, err := m.State(ctx); err != nil {
			// One retry through the link's own recovery. A failure that survives that is the kind the
			// whole exercise exists to count.
			if _, err2 := m.State(ctx); err2 != nil {
				r.PollFailures++
			}
		}
		r.Polls++

		now := time.Now()
		if now.Sub(lastPing) >= cfg.PingInterval {
			lastPing = now
			r.Pings++
			if err := m.Ping(ctx); err != nil {
				r.PingFailures++
			}
		}
		if now.Sub(lastIdentity) >= cfg.IdentityInterval {
			lastIdentity = now
			r.IdentityChecks++
			if m.Model() != r.Model || m.Firmware() != r.Firmware {
				r.IdentityMismatches++
			}
		}
		if cfg.NudgeInterval > 0 && now.Sub(lastNudge) >= cfg.NudgeInterval {
			lastNudge = now
			r.Nudges++
			// Out and straight back: the mount ends the night where it started, whatever happened in
			// between, so a soak can run unattended on a tracking rig.
			if err := m.Nudge(ctx, cfg.NudgeArcsec, 0); err != nil {
				r.MoveFailures++
			} else if err := m.Nudge(ctx, -cfg.NudgeArcsec, 0); err != nil {
				r.MoveFailures++
			}
		}
		if cfg.JogCheckInterval > 0 && now.Sub(lastJog) >= cfg.JogCheckInterval {
			lastJog = now
			r.JogTests++
			if err := m.jogDeadmanCheck(ctx); err != nil {
				r.MoveFailures++
			}
		}
		if now.Sub(hourStart) >= time.Hour {
			hourStart, resyncsAtHourStart = now, m.Health().Resyncs
		}
		if cfg.Progress != nil && now.Sub(lastProgress) >= time.Minute {
			lastProgress = now
			h := m.Health()
			fmt.Fprintf(cfg.Progress, "%s  polls %d  errors %d  resyncs %d  reconnects %d  p50 %dms  p99 %dms\n",
				now.Format("15:04:05"), r.Polls, h.Errors, h.Resyncs, h.Reconnects, h.LatencyP50Ms, h.LatencyP99Ms)
		}
	}
	return r.finish(m, start, hourStart, resyncsAtHourStart), nil
}

// jogDeadmanCheck starts the gentlest possible slew and then deliberately does not renew it, so the
// deadman has to be the thing that stops the mount. It is the only part of the soak that proves a
// safety mechanism rather than measuring one.
func (m *Mount) jogDeadmanCheck(ctx context.Context) error {
	if err := m.Jog(ctx, device.DirNorth, 1); err != nil {
		return err
	}
	deadline := time.Now().Add(m.deadmanWindow() + 3*time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			_ = m.Jog(ctx, device.DirNorth, 0)
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		m.expireStops()
		if m.stopsPending() == 0 {
			return nil
		}
	}
	// Belt and braces: if the deadman somehow did not fire, stop the axis by hand rather than leave
	// it turning because a test was inconclusive.
	_ = m.Jog(ctx, device.DirNorth, 0)
	return fmt.Errorf("the jog deadman did not stop the axis")
}

func (m *Mount) deadmanWindow() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deadmanLocked()
}

func (r SoakReport) finish(m *Mount, start, hourStart time.Time, resyncsAtHourStart uint64) SoakReport {
	r.Finished = time.Now()
	r.Link = m.Health()
	r.GoroutinesEnd = runtime.NumGoroutine()
	runtime.GC()
	r.HeapEndBytes = heapInUse()

	m.mu.Lock()
	r.LatencyP50Ms = m.stats.latency.percentileMs(0.50)
	r.LatencyP90Ms = m.stats.latency.percentileMs(0.90)
	r.LatencyP99Ms = m.stats.latency.percentileMs(0.99)
	r.LatencyMaxMs = m.stats.latency.maxMs
	r.LatencyMeanMs = m.stats.latency.meanMs()
	m.mu.Unlock()

	if r.Link.Resyncs >= resyncsAtHourStart {
		r.ResyncsLastHour = r.Link.Resyncs - resyncsAtHourStart
	}
	r.Failures = r.judge(start)
	return r
}

// judge applies the thresholds. Every line it produces names the number AND the limit, because the
// person reading it at breakfast did not watch the run.
func (r SoakReport) judge(start time.Time) []string {
	var f []string
	add := func(format string, args ...any) { f = append(f, fmt.Sprintf(format, args...)) }

	if r.PollFailures > 0 {
		add("%d state reads failed and did not recover (limit 0)", r.PollFailures)
	}
	if r.PingFailures > 0 {
		add("%d echo checks failed (limit 0) — the reply stream went out of step with the commands", r.PingFailures)
	}
	if r.MoveFailures > 0 {
		add("%d motion checks failed (limit 0)", r.MoveFailures)
	}
	if r.Link.Unrecovered > 0 {
		add("%d commands survived neither a resynchronisation nor a reconnect (limit 0)", r.Link.Unrecovered)
	}
	if r.Link.Desyncs > 0 {
		add("%d replies were proven to belong to the wrong command (limit 0)", r.Link.Desyncs)
	}
	if r.IdentityMismatches > 0 {
		add("the hand controller changed identity %d times (limit 0)", r.IdentityMismatches)
	}

	hours := r.Finished.Sub(start).Hours()
	if hours > 0 {
		if perHour := float64(r.Link.Resyncs) / hours; perHour > soakResyncsPerHour {
			add("%.1f resynchronisations per hour (limit %d)", perHour, soakResyncsPerHour)
		}
	}
	if hours >= 1 && r.ResyncsLastHour > 0 {
		add("%d resynchronisations in the final hour (limit 0) — the link degraded as the night went on", r.ResyncsLastHour)
	}
	if r.LatencyP50Ms > soakP50CeilingMs {
		add("median reply %d ms (limit %d)", r.LatencyP50Ms, soakP50CeilingMs)
	}
	if r.LatencyP99Ms > soakP99CeilingMs {
		add("99th-percentile reply %d ms (limit %d)", r.LatencyP99Ms, soakP99CeilingMs)
	}
	if r.LatencyMaxMs >= soakMaxCeilingMs {
		add("slowest reply %d ms — at or past the protocol's own ceiling, so it was a timeout", r.LatencyMaxMs)
	}
	if grew := r.GoroutinesEnd - r.GoroutinesStart; grew > soakGoroutineSlack {
		add("%d more goroutines than at the start (limit %d)", grew, soakGoroutineSlack)
	}
	if r.HeapEndBytes > r.HeapStartBytes+soakHeapGrowthBytes {
		add("heap grew by %d bytes after a collection (limit %d)", r.HeapEndBytes-r.HeapStartBytes, soakHeapGrowthBytes)
	}
	return f
}

// JudgeReconnects folds the reconnect count in, separately from judge so a deliberate unplug drill
// can allow some. A reconnect with the cable untouched is a defect; one the operator caused is data.
func (r *SoakReport) JudgeReconnects(allowed int) {
	if int(r.Link.Reconnects) > allowed {
		r.Failures = append(r.Failures,
			fmt.Sprintf("%d reconnects (limit %d) — with the cable untouched, any reconnect is a fault", r.Link.Reconnects, allowed))
	}
}

// JudgeCoverage checks the run actually did the work it claims, so a poller that quietly died cannot
// pass by having no failures.
func (r *SoakReport) JudgeCoverage(cfg SoakConfig) {
	cfg.applyDefaults()
	want := int(float64(r.Finished.Sub(r.Started)) / float64(cfg.PollInterval) * soakMinCommandFraction)
	if r.Polls < want {
		r.Failures = append(r.Failures,
			fmt.Sprintf("only %d state reads in %s (expected at least %d) — the run stopped polling", r.Polls, r.Finished.Sub(r.Started).Round(time.Second), want))
	}
}

func (r SoakReport) String() string {
	var b strings.Builder
	verdict := "PASS"
	if !r.Pass() {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "NexStar overnight soak — %s\n", verdict)
	fmt.Fprintf(&b, "  mount        %s  firmware %s  on %s\n", nonEmpty(r.Model, "?"), nonEmpty(r.Firmware, "?"), r.Path)
	fmt.Fprintf(&b, "  window       %s -> %s  (%s)\n",
		r.Started.Format("2006-01-02 15:04:05"), r.Finished.Format("2006-01-02 15:04:05"),
		r.Finished.Sub(r.Started).Round(time.Second))
	fmt.Fprintf(&b, "  work         %d polls  %d pings  %d nudges  %d jog checks  %d identity checks\n",
		r.Polls, r.Pings, r.Nudges, r.JogTests, r.IdentityChecks)
	fmt.Fprintf(&b, "  commands     %d  errors %d  retries %d  unrecovered %d\n",
		r.Link.Commands, r.Link.Errors, r.Link.Retries, r.Link.Unrecovered)
	fmt.Fprintf(&b, "  latency      p50 %dms  p90 %dms  p99 %dms  max %dms  mean %dms\n",
		r.LatencyP50Ms, r.LatencyP90Ms, r.LatencyP99Ms, r.LatencyMaxMs, r.LatencyMeanMs)
	fmt.Fprintf(&b, "  link         resyncs %d (%d in the last hour)  reconnects %d  proven desyncs %d\n",
		r.Link.Resyncs, r.ResyncsLastHour, r.Link.Reconnects, r.Link.Desyncs)
	fmt.Fprintf(&b, "  leaks        goroutines %d -> %d    heap %.1f -> %.1f MB\n",
		r.GoroutinesStart, r.GoroutinesEnd,
		float64(r.HeapStartBytes)/(1<<20), float64(r.HeapEndBytes)/(1<<20))
	if r.Link.LastError != "" {
		fmt.Fprintf(&b, "  last error   %s\n", r.Link.LastError)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "  note         %s\n", n)
	}
	if len(r.Failures) == 0 {
		b.WriteString("  thresholds   all met\n")
	}
	for _, x := range r.Failures {
		fmt.Fprintf(&b, "  - %s\n", x)
	}
	return b.String()
}

// JSON renders the report for a file, so a failed night is diagnosable beyond what the text shows.
func (r SoakReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func heapInUse() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapInuse
}
