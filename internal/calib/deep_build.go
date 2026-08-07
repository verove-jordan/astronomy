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
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// buildDeepBias pools this session's bias frames with every matching bias from prior sessions and
// stacks one deep master. Bias is sensor-only, so it is reused freely (no temp/recency bound). With
// NO raw frame on disk (all freed to S3), the existing library master serves instead — the same
// file previous runs stacked with, so calibration stays byte-identical without the raws.
func buildDeepBias(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	provider RawCalibProvider, libRows []Master, sig cameraSig, mastersDir, workDir string,
	stacks stackalg.MasterOptions, onProgress func(siril.Progress)) (Master, bool, string) {
	paths := localCalibPaths(inv, inspect.Bias, func(k inspect.SetKey) bool {
		return cameraSig{k.Gain, k.Offset, k.Bin} == sig
	})
	pool, err := provider.RawCalibPaths(ctx, RawQuery{Type: inspect.Bias, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin})
	if err != nil {
		return Master{}, false, "pool bias: " + err.Error()
	}
	paths = mergePaths(paths, pool, func(RawFrame) bool { return true })
	paths, warn := dropMissing(paths, "bias pool")
	paths, warn = dropNonFITS(paths, "bias pool", warn)
	if len(paths) == 0 {
		if m := libraryBiasFallback(libRows, sig); m != nil {
			return *m, true, joinWarn(warn, fmt.Sprintf(
				"bias g%do%d b%d: no raw frames on disk — using the library master (%d frames)",
				sig.Gain, sig.Offset, sig.Bin, m.FrameCount))
		}
		return Master{}, false, warn // no bias is a valid setup; only the ghost count (if any) is worth a note
	}

	key := inspect.SetKey{Type: inspect.Bias, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin}
	m, note, err := stackPooled(ctx, runner, MasterBias, key, paths, mastersDir, workDir, stacks, onProgress)
	if err != nil {
		return Master{}, false, joinWarn(warn, err.Error())
	}
	return m, true, joinWarn(warn, note)
}

// buildDeepDark pools this session's darks for one signature with every matching dark from prior
// sessions (same exposure/camera, temperature within tolerance, within the recency window) and
// stacks one deep master.
func buildDeepDark(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	provider RawCalibProvider, libRows []Master, sig darkSig, opts DeepOptions, mastersDir, workDir string,
	stacks stackalg.MasterOptions, onProgress func(siril.Progress)) (Master, bool, string) {
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
	paths, warn := dropMissing(paths, "dark pool")
	paths, warn = dropNonFITS(paths, "dark pool", warn)
	if len(paths) == 0 {
		// All raw darks freed to S3: the existing library master (stacked from those very frames on
		// an earlier run) keeps dark calibration byte-identical instead of silently skipping it.
		if m := libraryDarkFallback(libRows, sig, tol); m != nil {
			return *m, true, joinWarn(warn, fmt.Sprintf(
				"darks %dms g%do%d b%d @%dC: no raw frames on disk — using the library master (%d frames)",
				sig.ExposureMs, sig.Gain, sig.Offset, sig.Bin, sig.TempBucket, m.FrameCount))
		}
		return Master{}, false, joinWarn(warn, fmt.Sprintf("no darks available for %dms g%do%d b%d @%dC",
			sig.ExposureMs, sig.Gain, sig.Offset, sig.Bin, sig.TempBucket))
	}

	key := inspect.SetKey{
		Type: inspect.Dark, Gain: sig.Gain, Offset: sig.Offset, Bin: sig.Bin,
		ExposureMs: sig.ExposureMs, TempBucket: sig.TempBucket,
	}
	m, note, err := stackPooled(ctx, runner, MasterDark, key, paths, mastersDir, workDir, stacks, onProgress)
	if err != nil {
		return Master{}, false, joinWarn(warn, err.Error())
	}
	m.TempMilliC = int64(sig.TempBucket) * 1000
	m.HasTemp = true
	return m, true, joinWarn(warn, note)
}

// libraryBiasFallback picks the deepest on-disk library bias master matching the camera signature —
// the raws-freed substitute for an empty bias pool.
func libraryBiasFallback(rows []Master, sig cameraSig) *Master {
	var best *Master
	for i := range rows {
		m := &rows[i]
		if m.Type != MasterBias || m.Gain != sig.Gain || m.Offset != sig.Offset || m.Bin != sig.Bin {
			continue
		}
		if !fileExists(m.Path) {
			continue
		}
		if best == nil || m.FrameCount > best.FrameCount {
			best = m
		}
	}
	return best
}

// libraryDarkFallback picks the on-disk library dark master matching the signature (same exposure,
// temperature within tolerance): nearest temperature first, then the deepest stack.
func libraryDarkFallback(rows []Master, sig darkSig, tol float64) *Master {
	var best *Master
	bestDelta := math.MaxFloat64
	for i := range rows {
		m := &rows[i]
		if m.Type != MasterDark || m.Gain != sig.Gain || m.Offset != sig.Offset || m.Bin != sig.Bin ||
			m.ExposureMs != sig.ExposureMs {
			continue
		}
		delta := math.Abs(float64(m.TempMilliC-int64(sig.TempBucket)*1000)) / 1000
		if delta > tol || !fileExists(m.Path) {
			continue
		}
		if best == nil || delta < bestDelta || (delta == bestDelta && m.FrameCount > best.FrameCount) {
			best, bestDelta = m, delta
		}
	}
	return best
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

// dropMissing filters out pooled paths whose file is no longer on disk — e.g. raw frames freed after an
// S3 mirror, whose catalog rows survive. A single ghost would sink the whole Siril stack (LinkFrames
// symlinks blindly, and Siril aborts on "Opening image N failed"), losing every healthy frame with it.
// It returns the surviving paths plus a warning naming the ghost count ("" when none).
func dropMissing(paths []string, what string) ([]string, string) {
	ok := paths[:0]
	missing := 0
	for _, p := range paths {
		if fileExists(p) {
			ok = append(ok, p)
		} else {
			missing++
		}
	}
	if missing == 0 {
		return ok, ""
	}
	return ok, fmt.Sprintf("%s: skipped %d frame(s) missing on disk (freed to S3?)", what, missing)
}

// dropNonFITS filters out pooled paths Siril cannot link into a master sequence (only FITS belongs
// in the deep pools; a non-FITS row that slipped into the catalog — e.g. a processed TIFF once
// misclassified as calibration — would sink the whole stack with a bare `link: generic error`).
// The count is appended to warn so the run report says exactly what was excluded and why.
func dropNonFITS(paths []string, what, warn string) ([]string, string) {
	ok := paths[:0]
	skipped := 0
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".fit", ".fits", ".fts":
			ok = append(ok, p)
		default:
			skipped++
		}
	}
	if skipped == 0 {
		return ok, warn
	}
	return ok, joinWarn(warn, fmt.Sprintf("%s: skipped %d non-FITS file(s) (processed images are never stacked as calibration)", what, skipped))
}

// joinWarn joins two optional warning strings with "; ", tolerating either being empty.
func joinWarn(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
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
// the master metadata (frame count = pool size) plus an optional user-facing note.
func stackPooled(ctx context.Context, runner *siril.Runner, mt MasterType, key inspect.SetKey,
	paths []string, mastersDir, workDir string, stacks stackalg.MasterOptions,
	onProgress func(siril.Progress)) (Master, string, error) {
	stack := MasterStackOptions(mt, stacks)
	name := masterName(mt, key, stack)
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
		prevHash, prevCount := readPoolSig(outBase + ".sig")
		if prevHash == sig {
			if prevCount == 0 { // v1 sidecar (hash only) — record the pool depth for the shrink guard
				_ = os.WriteFile(outBase+".sig", []byte(formatPoolSig(sig, len(paths))), 0o644)
			}
			if mt == MasterDark && !fileExists(DefectsListPath(master.Path)) {
				_ = buildDefectList(master.Path, paths) // upgrade a pre-existing library master in place
			}
			return master, "", nil
		}
		// The pool SHRANK below the master's recorded depth (raw frames freed to S3): rebuilding
		// would overwrite a deep master with a shallower one. Keep the deeper master untouched —
		// the whole point of freeing local raws is that calibration stays byte-identical without them.
		if prevCount > 0 && len(paths) < prevCount {
			master.FrameCount = prevCount
			return master, fmt.Sprintf(
				"master %s: raw pool shrank to %d of %d frame(s) (freed to S3?) — kept the existing deeper master",
				name, len(paths), prevCount), nil
		}
	}
	seqDir := filepath.Join(workDir, "cal_"+name)
	if _, err := fsutil.LinkFrames(seqDir, paths); err != nil {
		return Master{}, "", err
	}
	// Stack into a run-unique hidden temp in the library dir, then atomically rename into place. With the
	// shared library, two concurrent runs building the same-signature master must never let one read the
	// other's half-written file; the rename publishes the whole master in one step (same filesystem).
	tmpBase := filepath.Join(mastersDir, ".tmp_"+filepath.Base(workDir)+"_"+name)
	if len(paths) == 1 {
		// A single-frame pool (S3-freed siblings) cannot be stacked — promote the lone frame
		// (see stackMasterSet; the same #352 trade).
		if err := promoteLoneCalFrame(ctx, runner, seqDir, tmpBase, onProgress); err != nil {
			return Master{}, "", fmt.Errorf("promote lone-frame master %s: %w", name, err)
		}
	} else if _, err := runner.Run(ctx, seqDir,
		siril.StackMasterScript("cal", tmpBase, len(paths), stack),
		onProgress); err != nil {
		return Master{}, "", fmt.Errorf("stack master %s: %w", name, err)
	}
	if err := os.Rename(tmpBase+".fits", outBase+".fits"); err != nil {
		return Master{}, "", fmt.Errorf("publish master %s: %w", name, err)
	}
	_ = os.WriteFile(outBase+".sig", []byte(formatPoolSig(sig, len(paths))), 0o644) // pool record for the next run's reuse + shrink checks
	if mt == MasterDark {
		_ = buildDefectList(master.Path, paths) // soft: its note is user-visible on the session-build path
	}
	_ = os.RemoveAll(seqDir)
	return master, "", nil
}

// formatPoolSig renders the .sig sidecar (v2): line 1 the pool content hash, line 2 the pool depth —
// the shrink guard's reference for "is this rebuild shallower than what produced the master?".
func formatPoolSig(hash string, count int) string { return hash + "\n" + strconv.Itoa(count) }

// readPoolSig parses a .sig sidecar of either version: v1 carries the hash alone (count 0 = unknown,
// upgraded in place on the next matching reuse), v2 adds the pool depth.
func readPoolSig(path string) (hash string, count int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	lines := strings.SplitN(strings.TrimSpace(string(b)), "\n", 3)
	hash = strings.TrimSpace(lines[0])
	if len(lines) > 1 {
		if n, err := strconv.Atoi(strings.TrimSpace(lines[1])); err == nil {
			count = n
		}
	}
	return hash, count
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
