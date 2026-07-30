// Per-pixel master dark/flat calibration for planetary frames — the lunar counterpart of the milkyway
// phone-calibration path (Go, in linear light). Siril `calibrate` is deliberately not used: the frames
// are already materialized as FITS in the channel scratch, so one in-process pass costs no extra disk
// (another Siril sequence would double the channel's scratch), and every failure soft-degrades to "less
// calibration" with a note instead of failing the run.
package planetary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// CalibSource supplies calibration masters to a planetary run. Build produces (or reuses) the masters
// for the scanned inventory — the pipeline wires it to the standard master machinery/library — with
// scratchDir as its build workspace (under the run's scratch root; library-less builds also keep their
// master FITS there, so it survives until the run ends). Exclude carries the user's per-set master
// exclusions (calib.SuggestID keys, Import panel parity). A nil CalibSource (or nil Build) runs the
// stack uncalibrated, exactly as before.
type CalibSource struct {
	Build   func(ctx context.Context, inv *inspect.Inventory, scratchDir string) ([]calib.Master, []string, error)
	Exclude []string
	// Force relaxes the matcher's gain/exposure/temperature gates so mismatched masters are applied anyway
	// (the force_calibration_frames override).
	Force bool
}

// RunExtras carries the optional wiring the pipeline layer provides to a planetary run: the full
// Import multi-select roots (nil → just the primary input path), the calibration source, and the
// overall-percent hook driving the job progress bar. A nil *RunExtras runs like the historical
// single-folder, uncalibrated, silent run — the CLI video path and the Siril MCP pass nil.
type RunExtras struct {
	InputDirs []string
	Calib     *CalibSource
	OnPercent func(p float64)
}

func (e *RunExtras) roots(primary string) []string {
	if e == nil || len(e.InputDirs) == 0 {
		return []string{primary}
	}
	return e.InputDirs
}

func (e *RunExtras) calib() *CalibSource {
	if e == nil {
		return nil
	}
	return e.Calib
}

func (e *RunExtras) onPercent() func(float64) {
	if e == nil {
		return nil
	}
	return e.OnPercent
}

// channelMasters holds one channel's loaded, scale-normalized masters: the subtraction term (dark,
// else bias) and the mean-normalized flat, each with its Master record for the run note.
type channelMasters struct {
	sub   *fits.Image
	subM  *calib.Master
	flat  *fits.Image
	flatM *calib.Master
}

func (cm channelMasters) empty() bool { return cm.sub == nil && cm.flat == nil }

// note renders the channel's calibration summary for Result.Notes.
func (cm channelMasters) note(filter string) string {
	var parts []string
	if cm.subM != nil {
		parts = append(parts, fmt.Sprintf("%s %dms g%d (%df)",
			strings.ToLower(string(cm.subM.Type)), cm.subM.ExposureMs, cm.subM.Gain, cm.subM.FrameCount))
	}
	if cm.flatM != nil {
		parts = append(parts, fmt.Sprintf("flat %s (%df)", flatName(cm.flatM), cm.flatM.FrameCount))
	}
	return fmt.Sprintf("calibrated %s: %s", channelLabel(filter), strings.Join(parts, " + "))
}

func flatName(m *calib.Master) string {
	if m.Filter == "" {
		return "session"
	}
	return m.Filter
}

// calibrateChannel applies the matched master dark/flat to a channel's materialized FITS frames,
// per-pixel: out = (light − dark) / normalizedFlat, clamped at 0. Frames that are regular files under
// the run scratch are rewritten in place; everything else (in-place source FITS, Siril's convert
// symlinks) gets a calibrated COPY in chDir so input captures are never mutated. onFrame (nil-safe)
// ticks per calibrated frame. The returned error is context cancellation only — every other failure
// degrades to a note and the original frames.
func calibrateChannel(ctx context.Context, frames []string, filter string, inv *inspect.Inventory,
	masters []calib.Master, exclude []string, force bool, chDir, scratchRoot string, onFrame func(done, total int)) ([]string, []string, error) {
	if len(frames) == 0 || len(masters) == 0 || inv == nil {
		return frames, nil, nil
	}
	key, notes, ok := dominantLightKey(inv, filter)
	if !ok {
		return frames, append(notes, fmt.Sprintf("channel %s uncalibrated: no classified light set", channelLabel(filter))), nil
	}
	sel := calib.MatchForLightExcluding(key, masters, exclude, force)
	if sel.DarkOptimize {
		// Dark optimization is Siril `-opt` semantics (scale a different-exposure dark's thermal signal);
		// the in-process subtraction cannot honor it, so refuse the mismatched dark rather than mis-subtract.
		sel.Dark, sel.DarkOptimize = nil, false
		notes = append(notes, fmt.Sprintf("channel %s: no %dms dark — different-exposure dark skipped (needs Siril dark-optimization)",
			channelLabel(filter), key.ExposureMs))
	}
	cm, loadNotes := loadChannelMasters(sel, frames[0])
	notes = append(notes, loadNotes...)
	if cm.empty() {
		return frames, append(notes, fmt.Sprintf("channel %s uncalibrated: no matching dark/flat", channelLabel(filter))), nil
	}
	out, applyNotes, err := applyCalibration(ctx, frames, cm, chDir, scratchRoot, onFrame)
	notes = append(notes, applyNotes...)
	if err != nil {
		return frames, notes, err
	}
	return out, append(notes, cm.note(filter)), nil
}

// dominantLightKey picks the channel's matching key: the largest classified Light set carrying this
// filter (the comet-pipeline pattern — never hand-build a SetKey, the bucketing lives in inspect).
func dominantLightKey(inv *inspect.Inventory, filter string) (inspect.SetKey, []string, bool) {
	var best *inspect.Set
	matching := 0
	for i := range inv.Sets {
		s := &inv.Sets[i]
		if s.Key.Type != inspect.Light || s.Key.Filter != filter {
			continue
		}
		matching++
		if best == nil || s.Count > best.Count {
			best = s
		}
	}
	if best == nil {
		return inspect.SetKey{}, nil, false
	}
	var notes []string
	if matching > 1 {
		notes = append(notes, fmt.Sprintf("channel %s spans %d capture sets (mixed gain/exposure/temp) — calibrating against the largest",
			channelLabel(filter), matching))
	}
	return best.Key, notes, true
}

// loadChannelMasters reads and preps the selected masters: dimension-guarded against the channel's
// first frame (sidecar TIFF dims are unknown until conversion, so the guard must run here), rescaled to
// the frames' [0,1] convention, flat normalized to mean 1. A master that fails any step is dropped with
// a note — never a run failure.
func loadChannelMasters(sel calib.Selection, refPath string) (channelMasters, []string) {
	var cm channelMasters
	var notes []string
	ref, err := fits.ReadImage(refPath)
	if err != nil {
		return cm, append(notes, "calibration skipped: read reference frame: "+err.Error())
	}
	load := func(m *calib.Master, role string) *fits.Image {
		if m == nil {
			return nil
		}
		im, err := fits.ReadImage(m.Path)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s master unreadable — %s correction skipped (%v)", role, role, err))
			return nil
		}
		if im.W != ref.W || im.H != ref.H || im.C != ref.C {
			notes = append(notes, fmt.Sprintf("%s master %dx%d does not match the %dx%d frames — %s correction skipped",
				role, im.W, im.H, ref.W, ref.H, role))
			return nil
		}
		normalizeScale(im)
		return im
	}
	subM, subRole := sel.Dark, "dark"
	if subM == nil {
		subM, subRole = sel.Bias, "bias"
	}
	if im := load(subM, subRole); im != nil {
		cm.sub, cm.subM = im, subM
	}
	if im := load(sel.Flat, "flat"); im != nil {
		if meanNormalizeFlat(im) {
			cm.flat, cm.flatM = im, sel.Flat
		} else {
			notes = append(notes, "flat master is ~empty — flat correction skipped")
		}
	}
	return cm, notes
}

// applyCalibration runs the per-frame (light − dark)/flat pass on bounded parallel workers. Frames are
// independent: one that fails to read/verify/write keeps its ORIGINAL path (it stacks uncalibrated —
// the σ-clip and the note cover the mix) and all failures aggregate into a single note. The returned
// error is context cancellation only.
func applyCalibration(ctx context.Context, frames []string, cm channelMasters, chDir, scratchRoot string,
	onFrame func(done, total int)) ([]string, []string, error) {
	if err := fsutil.EnsureDir(chDir); err != nil {
		return frames, []string{"calibration skipped: " + err.Error()}, nil
	}
	out := make([]string, len(frames))
	var mu sync.Mutex
	var failCount int
	var firstErr error
	var done atomic.Int64
	err := forEachFrame(ctx, len(frames), planetaryWorkers(), func(i int) error {
		dst, cerr := calibrateFrame(frames[i], i, cm, chDir, scratchRoot)
		if cerr != nil {
			mu.Lock()
			failCount++
			if firstErr == nil {
				firstErr = cerr
			}
			mu.Unlock()
			out[i] = frames[i] // the frame stacks uncalibrated
			return nil
		}
		out[i] = dst
		if onFrame != nil {
			onFrame(int(done.Add(1)), len(frames))
		}
		return nil
	})
	if err != nil {
		return frames, nil, err
	}
	var notes []string
	if failCount > 0 {
		notes = append(notes, fmt.Sprintf("calibration failed for %d/%d frame(s) — those stack uncalibrated (first: %v)",
			failCount, len(frames), firstErr))
	}
	return out, notes, nil
}

// calibrateFrame applies the loaded masters to one frame: scale-normalize, dark-subtract, flat-divide,
// clamp at 0, then write — in place when the file is a regular scratch file, else as a cal_ copy in
// chDir (named by input index, stable under parallel workers).
func calibrateFrame(p string, i int, cm channelMasters, chDir, scratchRoot string) (string, error) {
	im, err := fits.ReadImage(p)
	if err != nil {
		return "", err
	}
	if (cm.sub != nil && !dimsMatch(im, cm.sub)) || (cm.flat != nil && !dimsMatch(im, cm.flat)) {
		return "", fmt.Errorf("frame %dx%d does not match the masters", im.W, im.H)
	}
	normalizeScale(im)
	if cm.sub != nil {
		subtractPlanes(im, cm.sub)
	}
	if cm.flat != nil {
		divideFlat(im, cm.flat)
	}
	clampNonNegative(im)
	dst := p
	if !safeOverwrite(p, scratchRoot) {
		dst = filepath.Join(chDir, fmt.Sprintf("cal_%05d.fits", i+1))
	}
	if err := im.WriteFITS(dst); err != nil {
		return "", err
	}
	return dst, nil
}

// safeOverwrite reports whether p may be rewritten in place: a REGULAR file physically inside root.
// Siril `convert` SYMLINKS FITS sources instead of copying (and staged frames are symlinks too) —
// writing through either would corrupt the user's original capture, so those get copies instead.
func safeOverwrite(p, root string) bool {
	if !underDir(p, root) {
		return false
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// underDir reports whether p lies inside dir (both cleaned; p == dir does not count).
func underDir(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// dimsMatch reports whether two images share geometry (the per-pixel math requires it exactly).
func dimsMatch(a, b *fits.Image) bool {
	return a.W == b.W && a.H == b.H && a.C == b.C
}

// normalizeScale rescales ADU-range pixel data (16-bit-derived FITS read as 0..65535) onto the [0,1]
// float convention Siril's converted frames use, so frames and masters always mix on one scale.
// Data already in [0,1] is left untouched (mirrors nightscape's normalizeADU).
func normalizeScale(im *fits.Image) {
	var max float32
	for _, plane := range im.Pix {
		for _, v := range plane {
			if v > max {
				max = v
			}
		}
	}
	if max <= 1.5 {
		return
	}
	for _, plane := range im.Pix {
		for i := range plane {
			plane[i] /= 65535
		}
	}
}

// subtractPlanes subtracts sub from im per pixel (geometry pre-guarded by dimsMatch).
func subtractPlanes(im, sub *fits.Image) {
	for c := 0; c < len(im.Pix) && c < len(sub.Pix); c++ {
		p, s := im.Pix[c], sub.Pix[c]
		for i := range p {
			p[i] -= s[i]
		}
	}
}

// meanNormalizeFlat scales each flat plane to mean 1 — Siril's `-norm=mul` equalizes the flat FRAMES to
// each other, not the master to unity — so dividing preserves the light's photometric level. False when
// a plane is essentially empty (unusable flat).
func meanNormalizeFlat(flat *fits.Image) bool {
	for _, plane := range flat.Pix {
		var sum float64
		for _, v := range plane {
			sum += float64(v)
		}
		mean := sum / float64(len(plane))
		if mean <= 1e-6 {
			return false
		}
		scale := float32(1 / mean)
		for i := range plane {
			plane[i] *= scale
		}
	}
	return true
}

// divideFlat divides im by the normalized flat per pixel, skipping ~zero flat pixels (dead columns,
// extreme vignette) rather than blowing them up — mirrors the nightscape divide floor.
func divideFlat(im, flat *fits.Image) {
	for c := 0; c < len(im.Pix) && c < len(flat.Pix); c++ {
		p, f := im.Pix[c], flat.Pix[c]
		for i := range p {
			if f[i] > 1e-4 {
				p[i] /= f[i]
			}
		}
	}
}

// clampNonNegative floors every pixel at 0 (dark subtraction can push background pixels negative).
func clampNonNegative(im *fits.Image) {
	for _, plane := range im.Pix {
		for i := range plane {
			if plane[i] < 0 {
				plane[i] = 0
			}
		}
	}
}

// cleanupChannel frees a stacked channel's frame scratch — aligned warps, converted/staged frames — and
// keeps only its master (the finish still co-registers/deconvolves every channel master, ~65 MB each).
// Everything removed lives under the run's scratch root; in-place source FITS outside it are never touched.
func cleanupChannel(scratchRoot, chDir, framesDir string) {
	_ = os.RemoveAll(filepath.Join(chDir, "aligned"))
	if framesDir != "" && framesDir != chDir && underDir(framesDir, scratchRoot) {
		_ = os.RemoveAll(framesDir)
	}
	entries, err := os.ReadDir(chDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "master_") {
			continue
		}
		_ = os.Remove(filepath.Join(chDir, e.Name()))
	}
}
