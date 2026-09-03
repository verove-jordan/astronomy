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
// satCeil (optional, merged-frame order) additionally repairs sensor-saturated galaxy cores from
// the sub-ceiling median — the multi-night burn protection (transient/satmask.go).
func maskChannelTrails(seqDir, regSeq string, k float64, satCeil []float32) (*transient.Summary, string, error) {
	if k <= 0 && len(satCeil) == 0 {
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
	if len(satCeil) > len(paths) {
		// The registered space can be shorter than the merged order (frames Siril dropped). The
		// ceilings are order-aligned from the front; truncate defensively rather than misalign.
		satCeil = satCeil[:len(paths)]
	}

	// Memory gate: above the budget the streamed variant masks one frame at a time against a
	// bounded evenly-spaced basis instead of holding the whole sequence (see trailBasisPlan).
	//
	// The unit is the whole FRAME, not one plane. A debayered one-shot-colour frame is three W*H
	// float32 planes (fits.Image keeps Pix[channel]), and both fits.ReadImage and readStreamBasis
	// load all of them — so sizing from W*H*4 under-counts an OSC run by exactly 3×. That is what
	// killed this run: a 99-sub 26 MP colour stack took the streamed path but sized a 48-frame
	// basis (48 × 3 × 104 MiB ≈ 15 GiB), grew to 23.7 GiB, and the engine died and requeued the job
	// from step 1. With the channel count in, the same run budgets a 19-frame basis (~6 GiB).
	w, h := frameDims(paths[0])
	frameBytes := int64(w) * int64(h) * 4 * int64(frameChannels(paths[0]))
	budget := transient.MemBudget()
	if streamed, basisMax := trailBasisPlan(len(paths), frameBytes, budget); streamed {
		rep, err := transient.MaskCrossFrameStreamed(paths, k, basisMax, satCeil)
		if err != nil {
			return nil, "", err
		}
		mode := fmt.Sprintf("streamed, %d-frame basis under a %.1f GiB memory budget",
			basisMax, float64(budget)/float64(1<<30))
		return summarizeTrailMask(rep, len(paths), w, h, mode)
	}

	frames := make([]*fits.Image, len(paths))
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", filepath.Base(p), err)
		}
		frames[i] = im
	}
	rep, err := transient.MaskCrossFrameValidated(frames, k, satCeil)
	if err != nil {
		return nil, "", err
	}
	if s := rep.Summary(); s.MaskedPx > 0 || s.SatMaskedPx > 0 {
		for i, p := range paths {
			if rep.PerFrame[i].MaskedPx == 0 && rep.PerFrame[i].SatPx == 0 {
				continue // untouched frame: skip the rewrite
			}
			if err := frames[i].OverwriteData(p); err != nil {
				return nil, "", fmt.Errorf("write %s: %w", filepath.Base(p), err)
			}
		}
	}
	return summarizeTrailMask(rep, len(frames), frames[0].W, frames[0].H, "")
}

// summarizeTrailMask rolls a mask report into the run.json summary + channel note (nil/"" when
// nothing was masked). mode annotates the streamed budget path.
func summarizeTrailMask(rep *transient.Report, frames, w, h int, mode string) (*transient.Summary, string, error) {
	summary := rep.Summary()
	if summary.MaskedPx == 0 && summary.SatMaskedPx == 0 {
		return nil, "", nil
	}
	summary.Frames = frames
	suffix := ""
	if mode != "" {
		suffix = " (" + mode + ")"
	}
	pct := 100 * float64(summary.MaskedPx) / float64(frames*w*h)
	recurring := ""
	if summary.Recurring > 0 {
		recurring = fmt.Sprintf(", %d recurring corridor(s) repaired", summary.Recurring)
	}
	sat := ""
	if summary.SatMaskedPx > 0 {
		sat = fmt.Sprintf(", %d sensor-saturated core px repaired from unsaturated nights", summary.SatMaskedPx)
	}
	note := fmt.Sprintf("cross-frame trail mask: cleaned %d pixels (%.2f%%) across %d frames — %d trail segment(s) in %d frame(s), %d candidate(s) rejected as fixed pattern%s%s%s",
		summary.MaskedPx, pct, frames, summary.Segments, summary.WithTrails, summary.Rejected, recurring, sat, suffix)
	return &summary, note, nil
}

// trailBasisPlan decides whether the in-memory cross-frame pass fits in budget and, when it does
// not, how many frames the streamed basis may hold.
//
// frameBytes is the cost of ONE WHOLE FRAME in memory — every plane of it. The in-memory pass holds
// each frame plus a residual plane, ~2·n·frameBytes, and the 3× leaves GC and write-back slack.
//
// The streamed basis is floored at 16 (below that the fixed-pattern 30% fraction loses resolution)
// and capped at 48 (median/validation over 48 evenly-spaced subs is statistically equivalent to the
// full set for masking, and a budget-sized basis that approaches the whole sequence plus Go's GC
// slack was still enough to pressure the containerized stack's shared VM). The -6 reserves the
// working frame plus the recurring-corridor mean/count planes on top of the median/MAD pair.
func trailBasisPlan(nFrames int, frameBytes, budget int64) (streamed bool, basisMax int) {
	if frameBytes <= 0 || 3*int64(nFrames)*frameBytes <= budget {
		return false, 0
	}
	return true, min(max(int(budget/(2*frameBytes))-6, 16), 48, nFrames)
}
