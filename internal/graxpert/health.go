// Deep health probing. GraXpert can be installed yet unable to run its AI pipeline: a broken or
// missing onnxruntime makes every extraction fail at runtime ("Critical error! … No module named
// 'onnxruntime'") while the binary still resolves fine — and GraXpert even exits 0. A pipeline that
// gates its AI steps on a mere LookPath then commits to a path that always fails, which is worse
// than GraXpert being absent. Healthy answers "can GraXpert actually process an image right now" by
// running one tiny real background extraction, caching the verdict in-process and on disk.
package graxpert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	healthyTTL   = 24 * time.Hour   // a working install stays working
	unhealthyTTL = 15 * time.Minute // a just-fixed install should be picked up quickly
	probeTimeout = 10 * time.Minute // generous: the first probe may download GraXpert's AI model
	probeDim     = 64
)

// Healthy reports whether GraXpert can run its AI pipeline end-to-end. Unlike Available (a cheap
// binary lookup), it runs a real 64×64 background extraction once and caches the verdict — so the
// pipeline never commits to an AI path that fails at runtime. Blocking: the first call may take
// minutes (model download); concurrent callers wait for the same probe. Soft check by contract:
// callers log the error and fall back to the Siril path.
func (r *Runner) Healthy(ctx context.Context) error {
	if err := r.Available(ctx); err != nil {
		return err
	}
	if r.url != "" {
		// Offload mode: Available already confirmed the host service is reachable (its /health checks the
		// host binary). Treat that as healthy and memoize so the pipeline gate doesn't re-ping per channel;
		// a genuinely broken host GraXpert surfaces on the real Denoise/ExtractBackground call (soft-fail).
		r.healthMu.Lock()
		defer r.healthMu.Unlock()
		r.healthDone, r.healthErr = true, nil
		return nil
	}
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	if r.healthDone {
		return r.healthErr
	}
	if err, ok := readHealthCache(r.bin); ok {
		r.healthDone, r.healthErr = true, err
		return err
	}
	err := r.probe(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// The probe never ran to completion — report but do not memoize a verdict.
		return fmt.Errorf("graxpert health probe interrupted: %w", err)
	}
	r.healthDone, r.healthErr = true, err
	writeHealthCache(r.bin, err)
	return err
}

// HealthCached returns the last known health verdict without probing (for UI/status paths that
// cannot block). known=false means no verdict exists yet or a probe is currently in flight.
func (r *Runner) HealthCached() (verdict error, known bool) {
	if r == nil {
		return fmt.Errorf("graxpert runner is nil"), true
	}
	if r.url == "" && r.bin == "" {
		return fmt.Errorf("graxpert binary path is empty (set GRAXPERT_BIN)"), true
	}
	if !r.healthMu.TryLock() {
		return nil, false // a probe is running right now
	}
	defer r.healthMu.Unlock()
	if r.healthDone {
		return r.healthErr, true
	}
	if r.url == "" { // the on-disk cache is keyed on the local binary; offload mode has no local probe
		if err, ok := readHealthCache(r.bin); ok {
			r.healthDone, r.healthErr = true, err
			return err, true
		}
	}
	return nil, false
}

// probe runs one real background extraction on a tiny generated FITS. The probe image must carry
// texture: GraXpert normalizes by MAD, and a flat frame (MAD=0) yields an all-NaN "success".
func (r *Runner) probe(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "graxpert-health-")
	if err != nil {
		return fmt.Errorf("graxpert health probe: %w", err)
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "probe.fits")
	out := filepath.Join(dir, "probe_out.fits")
	if err := writeProbeFITS(in); err != nil {
		return fmt.Errorf("graxpert health probe: %w", err)
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeTimeout)
		defer cancel()
	}
	if err := r.run(ctx, backgroundArgs(in, out, BackgroundOptions{}), nil); err != nil {
		return fmt.Errorf("graxpert cannot run its AI pipeline: %w", err)
	}
	if _, err := os.Stat(out); err != nil {
		return errors.New("graxpert probe produced no output (AI runtime broken?)")
	}
	return nil
}

// writeProbeFITS writes a small mono frame with a gradient plus ripple — deterministic, nonzero MAD.
func writeProbeFITS(path string) error {
	im := fits.NewImage(probeDim, probeDim, 1)
	for y := 0; y < probeDim; y++ {
		for x := 0; x < probeDim; x++ {
			v := 0.1 + 0.4*float64(x+y)/float64(2*probeDim) +
				0.05*math.Sin(float64(x)*0.7)*math.Cos(float64(y)*0.5)
			im.Pix[0][y*probeDim+x] = float32(v)
		}
	}
	return im.WriteFITS(path)
}

// healthCacheRecord is the on-disk verdict, shared across processes (host engine + CLI runs).
type healthCacheRecord struct {
	OK        bool   `json:"ok"`
	Err       string `json:"err,omitempty"`
	CheckedMs int64  `json:"checked_ms"`
}

// healthCachePath keys the cache file by the resolved binary path and its mtime, so upgrading or
// reinstalling GraXpert invalidates the verdict automatically.
func healthCachePath(bin string) string {
	resolved, err := exec.LookPath(bin)
	if err != nil {
		resolved = bin
	}
	var mod int64
	if st, err := os.Stat(resolved); err == nil {
		mod = st.ModTime().UnixMilli()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", resolved, mod)))
	return filepath.Join(os.TempDir(), "astrostack-graxpert-health-"+hex.EncodeToString(sum[:8])+".json")
}

func readHealthCache(bin string) (verdict error, ok bool) {
	raw, err := os.ReadFile(healthCachePath(bin))
	if err != nil {
		return nil, false
	}
	var rec healthCacheRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, false
	}
	age := time.Since(time.UnixMilli(rec.CheckedMs))
	if rec.OK && age < healthyTTL {
		return nil, true
	}
	if !rec.OK && age < unhealthyTTL {
		return errors.New(rec.Err), true
	}
	return nil, false
}

func writeHealthCache(bin string, verdict error) {
	rec := healthCacheRecord{OK: verdict == nil, CheckedMs: time.Now().UnixMilli()}
	if verdict != nil {
		rec.Err = verdict.Error()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// Best-effort cache: a write failure only means the next process re-probes.
	_ = os.WriteFile(healthCachePath(bin), raw, 0o644)
}
