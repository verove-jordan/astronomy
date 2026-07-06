package calib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// buildDeepBias pools this session's bias frames with every matching bias from prior sessions and
// stacks one deep master. Bias is sensor-only, so it is reused freely (no temp/recency bound).
func buildDeepBias(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	provider RawCalibProvider, sig cameraSig, mastersDir, workDir string,
	onProgress func(siril.Progress)) (Master, bool, string) {
	paths := localCalibPaths(inv, inspect.Bias, func(k inspect.SetKey) bool {
		return cameraSig{k.Gain, k.Offset, k.Bin} == sig
	})
	pool, err := provider.RawCalibPaths(ctx, RawQuery{Type: inspect.Bias, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin})
	if err != nil {
		return Master{}, false, "pool bias: " + err.Error()
	}
	paths = mergePaths(paths, pool, func(RawFrame) bool { return true })
	if len(paths) == 0 {
		return Master{}, false, "" // no bias is a valid setup; nothing to warn about
	}

	key := inspect.SetKey{Type: inspect.Bias, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin}
	m, err := stackPooled(ctx, runner, MasterBias, key, paths, mastersDir, workDir, onProgress)
	if err != nil {
		return Master{}, false, err.Error()
	}
	return m, true, ""
}

// buildDeepDark pools this session's darks for one signature with every matching dark from prior
// sessions (same exposure/camera, temperature within tolerance, within the recency window) and
// stacks one deep master.
func buildDeepDark(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	provider RawCalibProvider, sig darkSig, opts DeepOptions, mastersDir, workDir string,
	onProgress func(siril.Progress)) (Master, bool, string) {
	tol := opts.tempTol()
	matchesTemp := func(f RawFrame) bool {
		if f.ExposureMs != sig.ExposureMs {
			return false
		}
		if !f.HasTemp {
			return true // no temp recorded — accept (best-effort), exposure already matched
		}
		return math.Abs(float64(f.TempMilliC-int64(sig.TempBucket)*1000))/1000 <= tol
	}
	paths := localCalibPaths(inv, inspect.Dark, func(k inspect.SetKey) bool {
		return cameraSig{k.Gain, k.Offset, k.Bin} == sig.cameraSig &&
			k.ExposureMs == sig.ExposureMs &&
			math.Abs(float64(k.TempBucket-sig.TempBucket)) <= tol
	})
	pool, err := provider.RawCalibPaths(ctx, RawQuery{
		Type: inspect.Dark, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin, SinceMs: opts.DarkSinceMs,
	})
	if err != nil {
		return Master{}, false, "pool darks: " + err.Error()
	}
	paths = mergePaths(paths, pool, matchesTemp)
	if len(paths) == 0 {
		return Master{}, false, fmt.Sprintf("no darks available for %dms g%do%d b%d @%dC",
			sig.ExposureMs, sig.Gain, sig.Offset, sig.Bin, sig.TempBucket)
	}

	key := inspect.SetKey{
		Type: inspect.Dark, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin,
		ExposureMs: sig.ExposureMs, TempBucket: sig.TempBucket,
	}
	m, err := stackPooled(ctx, runner, MasterDark, key, paths, mastersDir, workDir, onProgress)
	if err != nil {
		return Master{}, false, err.Error()
	}
	m.TempMilliC = int64(sig.TempBucket) * 1000
	m.HasTemp = true
	return m, true, ""
}

// localCalibPaths returns the paths of this session's calibration frames of a type matching pred.
func localCalibPaths(inv *inspect.Inventory, ft inspect.FrameType, pred func(inspect.SetKey) bool) []string {
	var out []string
	for _, set := range inv.SetsOfType(ft) {
		if pred(set.Key) {
			out = append(out, framePaths(set.Frames)...)
		}
	}
	return out
}

// mergePaths appends pool frames (passing keep) to base, de-duplicating by path.
func mergePaths(base []string, pool []RawFrame, keep func(RawFrame) bool) []string {
	seen := make(map[string]bool, len(base))
	for _, p := range base {
		seen[p] = true
	}
	out := append([]string(nil), base...)
	for _, f := range pool {
		if seen[f.Path] || !keep(f) {
			continue
		}
		seen[f.Path] = true
		out = append(out, f.Path)
	}
	return out
}

// stackPooled links the pooled frames into a sequence and stacks them into a master FITS, returning
// the master metadata (frame count = pool size).
func stackPooled(ctx context.Context, runner *siril.Runner, mt MasterType, key inspect.SetKey,
	paths []string, mastersDir, workDir string, onProgress func(siril.Progress)) (Master, error) {
	name := masterName(mt, key)
	outBase := filepath.Join(mastersDir, name)
	master := Master{
		Type: mt, Filter: key.Filter, ExposureMs: key.ExposureMs,
		Gain: key.Gain, Offset: key.Offset, Bin: key.Bin,
		FrameCount: len(paths), Path: outBase + ".fits",
	}
	// Reuse an existing master whose exact raw pool is unchanged (same frames, sizes, mtimes): the
	// Siril stack would be byte-identical, so a reprocess of the same session skips it (minutes on a
	// large dark/flat pool). The .sig sidecar records the pool that produced the on-disk master.
	sig := poolSignature(paths)
	if fileExists(outBase + ".fits") {
		if b, err := os.ReadFile(outBase + ".sig"); err == nil && string(b) == sig {
			return master, nil
		}
	}
	seqDir := filepath.Join(workDir, "cal_"+name)
	if _, err := fsutil.LinkFrames(seqDir, paths); err != nil {
		return Master{}, err
	}
	// Stack into a run-unique hidden temp in the library dir, then atomically rename into place. With the
	// shared library, two concurrent runs building the same-signature master must never let one read the
	// other's half-written file; the rename publishes the whole master in one step (same filesystem).
	tmpBase := filepath.Join(mastersDir, ".tmp_"+filepath.Base(workDir)+"_"+name)
	if _, err := runner.Run(ctx, seqDir, siril.StackMasterScript("cal", tmpBase), onProgress); err != nil {
		return Master{}, fmt.Errorf("stack master %s: %w", name, err)
	}
	if err := os.Rename(tmpBase+".fits", outBase+".fits"); err != nil {
		return Master{}, fmt.Errorf("publish master %s: %w", name, err)
	}
	_ = os.WriteFile(outBase+".sig", []byte(sig), 0o644) // record the pool for the next run's reuse check
	_ = os.RemoveAll(seqDir)
	return master, nil
}

// poolSignature is a stable content signature of a raw calibration pool: each frame's path, size and
// mtime, sorted. Unchanged frames → identical signature → an identical stacked master, so it can be
// reused instead of re-stacked.
func poolSignature(paths []string) string {
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			parts = append(parts, fmt.Sprintf("%s|%d|%d", p, fi.Size(), fi.ModTime().UnixNano()))
		} else {
			parts = append(parts, p+"|?")
		}
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}
