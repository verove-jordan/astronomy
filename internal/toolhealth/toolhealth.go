// Package toolhealth reports whether every external tool the pipeline drives can actually run —
// not merely whether its binary resolves. A present-but-broken tool (GraXpert with a broken ONNX
// runtime, a missing plate-solve catalogue) silently degrades results, so this report is surfaced
// in the UI BEFORE a run and stamped into run warnings, instead of being discovered from a bad
// image afterwards.
package toolhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// reportTTL caches the assembled report: probes spawn processes (siril-cli --version) and must not
// run on every poll of the status endpoint.
const reportTTL = 5 * time.Minute

// Tool is one external tool's health verdict.
type Tool struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"` // version / resolved kind / probe state
	Err    string `json:"err,omitempty"`
}

// PlateSolve describes the offline-solving situation: with a local Gaia astrometric catalogue,
// plate-solve (and therefore SPCC colour calibration) needs no network.
type PlateSolve struct {
	LocalGaiaAstro bool   `json:"local_gaia_astro"` // astrometric extract file present
	XpsampChunks   int    `json:"xpsamp_chunks"`    // local SPCC chunk files found
	LocalAsnet     bool   `json:"local_asnet"`      // local astrometry.net solving forced
	Catalog        string `json:"catalog"`          // effective platesolve -catalog value ("" = Siril default/online)
}

// Report is the full environment-health snapshot.
type Report struct {
	Siril      Tool       `json:"siril"`
	Gimp       Tool       `json:"gimp"`
	Graxpert   Tool       `json:"graxpert"`
	Starnet    Tool       `json:"starnet"`
	RawDev     Tool       `json:"raw_developer"`
	Devices    Tool       `json:"devices"` // the camera/mount device server (a separate process)
	LLM        Tool       `json:"llm"`
	PlateSolve PlateSolve `json:"plate_solve"`
	CheckedMs  int64      `json:"checked_ms"`
	Warnings   []string   `json:"warnings,omitempty"` // run-impacting problems, human-readable
}

// Checker assembles environment reports. It holds stable tool runners so probe memoization works,
// and caches the assembled report for reportTTL.
type Checker struct {
	cfg   *config.Config
	grax  *graxpert.Runner
	siril *siril.Runner
	llm   *llm.Runner

	mu     sync.Mutex
	cached *Report
	at     time.Time
}

// New builds a Checker from the runtime config.
func New(cfg *config.Config) *Checker {
	return &Checker{
		cfg:   cfg,
		grax:  graxpert.New(cfg.GraxpertBin, cfg.GraxpertURL),
		siril: siril.New(cfg.SirilBin, siril.Limits{}),
		llm:   llm.New(cfg.LLMBaseURL, cfg.LLMModel, cfg.LLMImageFormat),
	}
}

// Report returns the current environment health, cached for reportTTL. It never blocks on the slow
// GraXpert deep probe: an unknown verdict triggers the probe in the background and reports
// "probing" until it lands.
func (c *Checker) Report(ctx context.Context) *Report {
	c.mu.Lock()
	if c.cached != nil && time.Since(c.at) < reportTTL {
		defer c.mu.Unlock()
		return c.cached
	}
	c.mu.Unlock()

	r := c.collect(ctx)

	c.mu.Lock()
	c.cached, c.at = r, time.Now()
	c.mu.Unlock()
	return r
}

// Invalidate drops the cached report (e.g. after the GraXpert background probe completes).
func (c *Checker) Invalidate() {
	c.mu.Lock()
	c.cached = nil
	c.mu.Unlock()
}

func (c *Checker) collect(ctx context.Context) *Report {
	r := &Report{CheckedMs: time.Now().UnixMilli()}

	if err := c.siril.Available(ctx); err != nil {
		r.Siril = Tool{Err: err.Error()}
		r.Warnings = append(r.Warnings, "Siril is unavailable — no processing can run: "+err.Error())
	} else {
		r.Siril = Tool{OK: true}
	}

	// GIMP: a cheap binary lookup only. Client.Available() would START the GIMP server, which a
	// status endpoint must not do; a broken GIMP still soft-falls to Siril rgbcomp at run time.
	if _, err := exec.LookPath(c.cfg.GimpBin); err != nil {
		r.Gimp = Tool{Err: fmt.Sprintf("gimp binary %q not found", c.cfg.GimpBin)}
		r.Warnings = append(r.Warnings, "GIMP not found — deep-sky finishes fall back to a plain Siril composite")
	} else {
		r.Gimp = Tool{OK: true}
	}

	r.Graxpert = c.graxpertHealth()
	if r.Graxpert.Err != "" {
		r.Warnings = append(r.Warnings, "GraXpert cannot run its AI pipeline — gradients handled by Siril RBF subsky only ("+r.Graxpert.Err+")")
	}

	if _, err := exec.LookPath(c.cfg.StarnetBin); err != nil {
		r.Starnet = Tool{Err: fmt.Sprintf("starnet binary %q not found", c.cfg.StarnetBin)}
	} else {
		r.Starnet = Tool{OK: true}
	}

	r.Devices = c.deviceHealth(ctx)

	if kind, err := rawconv.Developer(); err != nil {
		r.RawDev = Tool{Err: err.Error()}
		r.Warnings = append(r.Warnings, "no raw developer — iPhone DNG (milky way) inputs cannot be processed: "+err.Error())
	} else {
		r.RawDev = Tool{OK: true, Detail: kind}
		if kind == "sips" {
			r.Warnings = append(r.Warnings, "raw develop uses macOS sips (Apple tone curve) — install LibRaw (`brew install libraw`) for exact linear milky-way processing")
		}
	}

	if c.cfg.LLMBaseURL == "" {
		r.LLM = Tool{Err: "no model server configured (ASTRO_LLM_URL)"}
	} else if err := c.llm.Available(ctx); err != nil {
		r.LLM = Tool{Err: err.Error()}
	} else {
		r.LLM = Tool{OK: true, Detail: c.cfg.LLMModel}
	}

	r.PlateSolve = PlateSolve{
		LocalGaiaAstro: c.cfg.LocalGaiaAstroCat() != "",
		XpsampChunks:   countXpsampChunks(c.cfg.GaiaXpsampDir),
		LocalAsnet:     c.cfg.LocalAsnet,
		Catalog:        effectiveCatalog(c.cfg),
	}
	if !r.PlateSolve.LocalGaiaAstro && !r.PlateSolve.LocalAsnet {
		r.Warnings = append(r.Warnings, "no local plate-solve catalogue — solving needs network and SPCC colour calibration may fall back (run `just download-catalogues`)")
	}
	return r
}

// graxpertHealth reports the cached GraXpert deep-probe verdict without blocking; an unknown
// verdict kicks the probe in the background and reads as "probing".
func (c *Checker) graxpertHealth() Tool {
	if err := c.grax.Available(context.Background()); err != nil {
		return Tool{Err: err.Error()}
	}
	verdict, known := c.grax.HealthCached()
	if !known {
		go func() {
			_ = c.grax.Healthy(context.Background())
			c.Invalidate() // next report picks up the fresh verdict
		}()
		return Tool{Detail: "probing"}
	}
	if verdict != nil {
		return Tool{Err: verdict.Error()}
	}
	return Tool{OK: true}
}

func countXpsampChunks(dir string) int {
	if dir == "" {
		return 0
	}
	matches, err := filepath.Glob(filepath.Join(dir, "siril_cat*_xpsamp_*.dat"))
	if err != nil {
		return 0
	}
	return len(matches)
}

// effectiveCatalog mirrors the siril platesolveCmd default: an explicit config wins, else localgaia
// when the local astrometric catalogue is installed, else Siril's own (online) default.
func effectiveCatalog(cfg *config.Config) string {
	if cfg.PlateSolveCatalog != "" {
		return cfg.PlateSolveCatalog
	}
	if cfg.LocalGaiaAstroCat() != "" {
		return "localgaia"
	}
	return ""
}

// deviceHealth probes the device server — a SEPARATE process (`just device`) that owns the camera,
// filter wheel and mount. It is normal for it not to be running (nothing is plugged in, or the user
// is only processing), so a refused connection is reported as a plain "not running", never as a
// warning that would clutter a processing-only session.
func (c *Checker) deviceHealth(ctx context.Context) Tool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.cfg.DeviceAddr+"/health", nil)
	if err != nil {
		return Tool{Err: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Tool{Err: "not running (start it with `just device`)"}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Tool{Err: "device server returned " + resp.Status}
	}
	var body struct {
		Drivers []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
		} `json:"drivers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Tool{OK: true, Detail: "running"}
	}
	names := make([]string, 0, len(body.Drivers))
	for _, d := range body.Drivers {
		if d.Available {
			names = append(names, d.Name)
		}
	}
	return Tool{OK: true, Detail: "drivers: " + strings.Join(names, ", ")}
}
