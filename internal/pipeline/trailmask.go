package pipeline

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/transient"
)

// maskChannelTrails removes cross-frame transients (satellite/plane trail segments, cosmic rays, hot
// pixels) from a channel's REGISTERED sub-frames before they are stacked. It loads every
// <regSeq>_NNNNN.fits in seqDir, runs transient.MaskCrossFrameValidated (the blanket per-pixel
// median-outlier replacement PLUS a fixed-pattern-validated line pass that paints a real trail's faint
// sub-threshold wings — the part the per-pixel threshold misses), and writes the cleaned pixels back in
// place (preserving each frame's header). This catches a slow/faint satellite that lands in many subs at
// marching positions — which per-frame trail rejection cannot drop without losing the whole channel, and
// a normal stack sigma-clip is too loose to remove — at no global SNR cost. The line pass validates each
// candidate against the other frames so it never repaints fixed-pattern walking noise (the reason the
// naive line mask was reverted); the unvalidated transient.MaskSequence stays in the tree, unused here.
//
// All registered frames are masked, including any later graded out of the stack: masking them is
// harmless and a larger sample makes the per-pixel median more robust. Returns a compact summary (for
// run.json) and a human-readable note (nil/"" when it did nothing). Memory: holds the channel's
// registered frames at once (~64 MB each); channels are processed sequentially so only one channel is
// resident.
func maskChannelTrails(seqDir, regSeq string, k float64) (*transient.Summary, string, error) {
	if k <= 0 {
		return nil, "", nil
	}
	paths, err := filepath.Glob(filepath.Join(seqDir, regSeq+"_*.fits"))
	if err != nil {
		return nil, "", err
	}
	if len(paths) < transient.MinFrames {
		return nil, "", nil // too few frames for a robust per-pixel statistic
	}
	sort.Strings(paths)

	frames := make([]*fits.Image, len(paths))
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", filepath.Base(p), err)
		}
		frames[i] = im
	}
	rep, err := transient.MaskCrossFrameValidated(frames, k)
	if err != nil {
		return nil, "", err
	}
	summary := rep.Summary()
	if summary.MaskedPx == 0 {
		return nil, "", nil
	}
	for i, p := range paths {
		if rep.PerFrame[i].MaskedPx == 0 {
			continue // untouched frame: skip the rewrite
		}
		if err := frames[i].OverwriteData(p); err != nil {
			return nil, "", fmt.Errorf("write %s: %w", filepath.Base(p), err)
		}
	}
	summary.Frames = len(frames)
	pct := 100 * float64(summary.MaskedPx) / float64(len(frames)*frames[0].W*frames[0].H)
	note := fmt.Sprintf("cross-frame trail mask: cleaned %d pixels (%.2f%%) across %d frames — %d trail segment(s) in %d frame(s), %d candidate(s) rejected as fixed pattern",
		summary.MaskedPx, pct, len(frames), summary.Segments, summary.WithTrails, summary.Rejected)
	return &summary, note, nil
}
