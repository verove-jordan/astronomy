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
	"os"
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
	Siril    Tool `json:"siril"`
	Gimp     Tool `json:"gimp"`
	Graxpert Tool `json:"graxpert"`
	Starnet  Tool `json:"starnet"`
	// FFmpeg carries ffprobe's resolution in its Detail: the two always ship together, and ffprobe
	// is looked up as ffmpeg's sibling when FFPROBE_BIN is unset, so reporting them apart would
	// invite fixing one and leaving the other.
	FFmpeg     Tool       `json:"ffmpeg"`
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

	if ver, err := c.siril.Version(ctx); err != nil {
		r.Siril = Tool{Err: err.Error()}
		r.Warnings = append(r.Warnings, "Siril is unavailable — no processing can run: "+err.Error())
	} else {
		r.Siril = Tool{OK: true, Detail: ver}
	}

	r.FFmpeg = c.ffmpegHealth(ctx)
	if r.FFmpeg.Err != "" {
		r.Warnings = append(r.Warnings, "ffmpeg not found — video output and the planetary/sun/eclipse "+
			"frame ingest cannot run ("+r.FFmpeg.Err+")")
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
		// Said out loud, like every other optional tool. StarNet's absence used to be reported as a
		// bare ok:false with no warning, which is the one form the UI banner does not render — so the
		// nebula/comet star-reduction step silently did nothing and the picture merely looked
		// different. It is never bundled (the licence is not redistributable), so on a container run
		// it is ALWAYS absent unless the user mounted it, which makes saying so more important here
		// rather than less.
		r.Warnings = append(r.Warnings, "StarNet++ not found — star reduction is skipped and the "+
			"nebula/comet finishes keep full stars (mount it and set STARNET_BIN)")
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

// ffmpegHealth resolves ffmpeg and reads its version.
//
// It is here because ffmpeg was the one tool the pipeline treats as REQUIRED and nothing ever
// checked: every other external binary is guarded by a LookPath somewhere, while ffmpeg is invoked
// directly by the planetary, solar and video-output paths and fails mid-run as `ffmpeg extract: …`,
// twenty minutes into a job. A missing ffprobe is reported alongside but is not itself an error —
// the extractors degrade to 8-bit rather than stopping.
func (c *Checker) ffmpegHealth(ctx context.Context) Tool {
	bin := c.cfg.FfmpegBin
	if bin == "" {
		bin = "ffmpeg"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return Tool{Err: fmt.Sprintf("ffmpeg binary %q not found", bin)}
	}
	detail := ffmpegVersion(ctx, path)
	if _, err := exec.LookPath(ffprobeBin(path)); err != nil {
		detail += " (no ffprobe — video probing degrades to 8-bit)"
	}
	return Tool{OK: true, Detail: detail}
}

// ffprobeBin mirrors how the video probers resolve ffprobe: FFPROBE_BIN, else ffmpeg's sibling.
func ffprobeBin(ffmpegPath string) string {
	if v := os.Getenv("FFPROBE_BIN"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(ffmpegPath), "ffprobe")
}

// ffmpegVersion returns just the version token from `ffmpeg -version`, whose first line reads
// "ffmpeg version 7.1 Copyright …". A probe that will not run is not fatal here — the binary
// resolved, which is what the pipeline needs — so this degrades to the empty string.
func ffmpegVersion(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) >= 3 && fields[0] == "ffmpeg" && fields[1] == "version" {
		return fields[2]
	}
	return ""
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
