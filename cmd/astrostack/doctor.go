package main

// doctor.go answers "is this machine actually able to run AstroStack, and what will silently
// degrade if I start a job now?".
//
// It reports rather than installs. Every external tool here is invoked, never bundled — their
// licences stay with the user's own install — so the useful thing a command can do is name what is
// missing, say what that costs, and name the fix. The probing itself is NOT reimplemented here:
// internal/toolhealth already runs the deep probes behind GET /api/environment (a real
// `siril-cli --version`, a real GraXpert extraction), so this is a renderer over that report plus
// the one check the in-engine report has no reason to make — whether Postgres answers.
//
// It runs in both worlds. On the host it describes the Mac's own Siril/GIMP; inside the engine
// container `just stack` runs it through `docker compose exec` and it describes the Linux tools
// baked into the image. Same command, same output, so the two setups can be compared at a glance.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/toolhealth"
)

// graxpertSettleTimeout bounds the wait for GraXpert's verdict. Its probe is deliberately
// asynchronous — a status endpoint must not block on a real extraction — and reports "probing"
// until it lands. A one-shot command has the opposite priority: an answer is the whole point, so it
// waits, but not forever.
const graxpertSettleTimeout = 30 * time.Second

// dbPingTimeout bounds the Postgres check. It is short because a missing database is the ordinary
// case on a first run (`just up` has not happened yet), not an exceptional one worth hanging for.
const dbPingTimeout = 3 * time.Second

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the raw environment report as JSON")
	quiet := fs.Bool("quiet", false, "print only the warnings (nothing when everything is fine)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg := config.Load()
	ctx := context.Background()

	rep := settledReport(ctx, cfg)
	db := postgresTool(ctx, cfg.DatabaseURL)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			*toolhealth.Report
			Postgres toolhealth.Tool `json:"postgres"`
		}{rep, db})
	}

	if !*quiet {
		printReport(rep, db, cfg)
	} else {
		printWarnings(rep, db)
	}

	// Siril is the only hard failure: without it no mode can stack anything. Everything else has a
	// documented fallback, so a warning is the honest severity and a non-zero exit would be a lie.
	if !rep.Siril.OK {
		return fmt.Errorf("no usable Siril — no processing can run until that is fixed")
	}
	return nil
}

// settledReport collects the environment report, waiting out GraXpert's background probe so the
// output says ok or broken rather than "probing".
func settledReport(ctx context.Context, cfg *config.Config) *toolhealth.Report {
	checker := toolhealth.New(cfg)
	deadline := time.Now().Add(graxpertSettleTimeout)
	for {
		rep := checker.Report(ctx)
		if rep.Graxpert.Detail != "probing" || time.Now().After(deadline) {
			return rep
		}
		// The probe runs a real extraction on a background goroutine and invalidates the cache when
		// it lands; poll for that rather than re-probing.
		time.Sleep(500 * time.Millisecond)
	}
}

// postgresTool reports whether the database the engine needs is reachable. It is checked here and
// not in toolhealth because that report is assembled INSIDE a running engine, which by then has
// already connected — the question only means something before the engine starts.
func postgresTool(ctx context.Context, dsn string) toolhealth.Tool {
	ctx, cancel := context.WithTimeout(ctx, dbPingTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return toolhealth.Tool{Err: err.Error()}
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return toolhealth.Tool{Err: "not reachable — start it with `just up` (" + err.Error() + ")"}
	}
	var version string
	if err := pool.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		return toolhealth.Tool{OK: true}
	}
	return toolhealth.Tool{OK: true, Detail: version}
}

// printReport renders the whole environment, grouped by what its absence actually costs. The
// grouping is the point: a flat list makes a missing StarNet++ look as alarming as a missing Siril,
// and the two are not remotely the same problem.
func printReport(rep *toolhealth.Report, db toolhealth.Tool, cfg *config.Config) {
	fmt.Println()
	fmt.Println("Required — nothing runs without these")
	line("Siril", rep.Siril, cfg.SirilBin)
	line("ffmpeg", rep.FFmpeg, cfg.FfmpegBin)
	line("Postgres", db, cfg.DatabaseURL)

	fmt.Println()
	fmt.Println("Recommended — present, or the result is visibly worse")
	line("GIMP", rep.Gimp, cfg.GimpBin)
	line("raw developer", rep.RawDev, "sips / dcraw_emu")

	fmt.Println()
	fmt.Println("Optional — each one soft-fails to a documented fallback")
	line("GraXpert", rep.Graxpert, cfg.GraxpertBin)
	line("StarNet++", rep.Starnet, cfg.StarnetBin)
	line("local AI model", rep.LLM, cfg.LLMBaseURL)
	line("device server", rep.Devices, cfg.DeviceAddr)

	fmt.Println()
	fmt.Println("Plate solving")
	fmt.Printf("  %s %-16s %s\n", mark(rep.PlateSolve.LocalGaiaAstro || rep.PlateSolve.LocalAsnet),
		"catalogue", plateSolveDetail(rep.PlateSolve))

	printWarnings(rep, db)
}

// printWarnings prints toolhealth's own prose verbatim. Those strings are written to be read by a
// person and already name the consequence and the fix, so restating them here would only let the
// two drift apart.
func printWarnings(rep *toolhealth.Report, db toolhealth.Tool) {
	warnings := rep.Warnings
	if db.Err != "" {
		warnings = append([]string{"Postgres " + db.Err}, warnings...)
	}
	if len(warnings) == 0 {
		fmt.Println("\nEverything checks out.")
		return
	}
	fmt.Println()
	for _, w := range warnings {
		fmt.Printf("  ⚠ %s\n", wrapIndent(w, 92, "    "))
	}
}

// line prints one tool's verdict. The configured path is shown only when the tool is MISSING —
// when it works, the path is noise; when it does not, it is almost always the answer (a stale
// SIRIL_BIN pointing into an app bundle that was moved).
func line(name string, t toolhealth.Tool, configured string) {
	detail := t.Detail
	if !t.OK {
		detail = t.Err
		if configured != "" && !strings.Contains(detail, configured) {
			detail += "  [" + configured + "]"
		}
	}
	if detail == "" {
		fmt.Printf("  %s %s\n", mark(t.OK), name)
		return
	}
	fmt.Printf("  %s %-16s %s\n", mark(t.OK), name, detail)
}

func mark(ok bool) string {
	if ok {
		return "✓"
	}
	return "⚠"
}

// plateSolveDetail describes the offline-solving situation in one line: with a local Gaia
// catalogue, solving and SPCC need no network at all.
func plateSolveDetail(p toolhealth.PlateSolve) string {
	switch {
	case p.LocalAsnet:
		return "local astrometry.net"
	case p.LocalGaiaAstro && p.XpsampChunks > 0:
		return fmt.Sprintf("local Gaia astrometry + %d SPCC chunk(s) — solving works offline", p.XpsampChunks)
	case p.LocalGaiaAstro:
		return "local Gaia astrometry — solving works offline, SPCC still needs network"
	default:
		return "none — solving needs network (`just download-catalogues`)"
	}
}

// wrapIndent soft-wraps a warning at width, indenting continuation lines so a two-line warning
// still reads as one item rather than as two.
func wrapIndent(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	var b strings.Builder
	lineLen := 0
	for i, w := range words {
		switch {
		case i == 0:
			b.WriteString(w)
			lineLen = len(w)
		case lineLen+1+len(w) > width:
			b.WriteString("\n" + indent + w)
			lineLen = len(indent) + len(w)
		default:
			b.WriteString(" " + w)
			lineLen += 1 + len(w)
		}
	}
	return b.String()
}
