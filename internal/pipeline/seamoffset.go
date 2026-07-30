package pipeline

import (
	"context"
	"fmt"
	"math"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/photom"
)

// The whole-frame photometric normalization compares different sky/object mixes when session
// footprints rotate, and its identity epsilon (0.2·σ_sub) passes pedestal residuals that stack
// into 2–6·σ_master steps — the straight "cut lines" of multi-night masters (task #354/#355). The
// seam offset refit re-measures each non-anchor group INSIDE its registered footprint overlap with
// the anchor group — both sides restricted to the SAME sky region — and applies the residual
// offset to the group's merged-sequence frames before seqapplyreg writes registered pixels.
// Offset-only by design: flux scales are the normalization ladder's job; sky pedestal steps are
// additive. Soft-fail throughout: any guard or error leaves the frames untouched with a reason.
const (
	// seamOffsetEpsSub: corrections below this fraction of the anchor's overlap noise are invisible
	// after stacking (0.02·σ_sub ≈ 0.2·σ_master at N≈100) — skip, the frames stay untouched.
	seamOffsetEpsSub = 0.02
	// seamOffsetMaxBgFrac caps a correction at this fraction of the anchor's overlap background —
	// a bigger "pedestal" means the overlap statistics are not measuring sky; degrade to no-op.
	seamOffsetMaxBgFrac = 0.5
	// seamContamSigmaK bounds the disagreement between the background-level delta and the deep-low
	// (P20) delta: extended signal filling the overlap skews the probes unevenly and would
	// masquerade as a pedestal step.
	seamContamSigmaK = 0.5
	// seamMinOverlapFrac is the overlap fraction of a group's footprint below which the overlap
	// statistics are too thin to trust.
	seamMinOverlapFrac = 0.25
	// seamMaxProbeFrames caps how many frames per group are measured (evenly spaced).
	seamMaxProbeFrames = 7
)

// SeamRepair aggregates one channel's seam-repair outcomes (run.json provenance).
type SeamRepair struct {
	Offsets []SeamOffset `json:"offsets,omitempty"`
	NoiseEq *SeamNoiseEq `json:"noise_eq,omitempty"`
}

// SeamOffset is one non-anchor group's overlap-refit outcome.
type SeamOffset struct {
	Session string  `json:"session,omitempty"`
	Delta   float64 `json:"delta"`
	Applied bool    `json:"applied"`
	Reason  string  `json:"reason,omitempty"` // why the correction was skipped ("" when applied)
}

// seamOffsetDelta returns the additive pedestal correction mapping a group's overlap sky onto the
// anchor's (Δ = anchor.Bg − group.Bg), or a non-empty reason to leave the group untouched.
func seamOffsetDelta(group, anchor photom.FrameCurve) (float64, string) {
	dBg := anchor.Bg - group.Bg
	if math.Abs(dBg) <= seamOffsetEpsSub*anchor.Noise {
		return 0, "pedestal below stack visibility"
	}
	dLow := anchor.Q[2] - group.Q[2] // P20: deep in the sky distribution on both sides
	if math.Abs(dBg-dLow) > seamContamSigmaK*anchor.Noise {
		return 0, "overlap contaminated by extended signal (background and P20 deltas disagree)"
	}
	maxDelta := seamOffsetMaxBgFrac * anchor.Bg
	if maxDelta <= 0 {
		maxDelta = 3 * anchor.Noise // near-zero anchor sky: bound by noise instead of the background
	}
	if math.Abs(dBg) > maxDelta {
		return 0, fmt.Sprintf("correction %+.4g beyond the sanity cap (%.4g)", dBg, maxDelta)
	}
	if group.Bg+dBg-3*group.Noise < 0 {
		return 0, "correction would clip the group's sky below zero"
	}
	return dBg, ""
}

// refitGroupOffsets measures every non-anchor group against the anchor group inside their mutual
// footprint overlap (canvas space, from the review homographies) and rewrites the group's
// merged-sequence frames in place (p += Δ) before seqapplyreg. Registration is untouched — an
// additive constant cannot move the stored transforms.
func refitGroupOffsets(ctx context.Context, opts Options, ch *ChannelResult, groups []lightGroup,
	spans []groupSpan, anchorIdx int, review registrationReview, mergedDir, seqBase, filter string,
	fw, fh int, ref stepRef) {
	if anchorIdx < 0 || anchorIdx >= len(spans) || len(review.FrameH) == 0 {
		return
	}
	rejected := func(i int) bool { _, ok := review.FrameH[i]; return !ok }
	cv := canvasSpec{W: fw, H: fh}
	anchorMask := groupFootprintMask(review.FrameH, rejected, spans[anchorIdx], fw, fh, cv, coverageDownscale)
	gridW := (fw + coverageDownscale - 1) / coverageDownscale
	gridH := (fh + coverageDownscale - 1) / coverageDownscale

	opts.sessionLine(ref, "", fmt.Sprintf("▶ seam offset refit %s (%d groups vs anchor %s)", filter, len(groups)-1, groups[anchorIdx].Session))
	seam := &SeamRepair{}
	for gi := range groups {
		if gi == anchorIdx || gi >= len(spans) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		rec := refitOneGroup(ctx, opts, groups[gi], spans[gi], spans[anchorIdx], anchorMask,
			review, mergedDir, seqBase, cv, gridW, gridH)
		seam.Offsets = append(seam.Offsets, rec)
		opts.sessionLine(ref, rec.Session, seamOffsetLine(rec))
	}
	ch.Seam = seam
	opts.sessionLine(ref, "", fmt.Sprintf("✓ seam offset refit %s done", filter))
}

// refitOneGroup measures one group's overlap curve against the anchor's over the SAME region and
// applies the guarded offset to the group's kept frames.
func refitOneGroup(ctx context.Context, opts Options, g lightGroup, span, anchorSpan groupSpan,
	anchorMask []bool, review registrationReview, mergedDir, seqBase string,
	cv canvasSpec, gridW, gridH int) SeamOffset {
	rec := SeamOffset{Session: g.Session}
	rejected := func(i int) bool { _, ok := review.FrameH[i]; return !ok }
	groupMask := groupFootprintMask(review.FrameH, rejected, span, cv.W, cv.H, cv, coverageDownscale)
	overlap, overlapFrac := intersectWithFrac(anchorMask, groupMask)
	if overlapFrac < seamMinOverlapFrac {
		rec.Reason = fmt.Sprintf("footprint overlap with the anchor too small (%.0f%%)", overlapFrac*100)
		return rec
	}
	groupCurve, ok := overlapCurve(ctx, review, span, overlap, mergedDir, seqBase, cv, gridW, gridH)
	if !ok {
		rec.Reason = "overlap region too thin to measure on the group's frames"
		return rec
	}
	anchorCurve, ok := overlapCurve(ctx, review, anchorSpan, overlap, mergedDir, seqBase, cv, gridW, gridH)
	if !ok {
		rec.Reason = "overlap region too thin to measure on the anchor's frames"
		return rec
	}
	delta, reason := seamOffsetDelta(groupCurve, anchorCurve)
	if reason != "" {
		rec.Reason = reason
		return rec
	}
	if err := addOffsetToFrames(ctx, mergedDir, seqBase, keptIndices(review, span), delta); err != nil {
		rec.Reason = "apply failed: " + err.Error()
		return rec
	}
	rec.Delta, rec.Applied = delta, true
	return rec
}

// overlapCurve is the component-median masked curve of up to seamMaxProbeFrames evenly-spaced kept
// frames of the span, each restricted (through its own homography) to the overlap region.
func overlapCurve(ctx context.Context, review registrationReview, span groupSpan, overlap []bool,
	mergedDir, seqBase string, cv canvasSpec, gridW, gridH int) (photom.FrameCurve, bool) {
	var curves []photom.FrameCurve
	for _, idx := range evenSpacedInts(keptIndices(review, span), seamMaxProbeFrames) {
		if ctx.Err() != nil {
			return photom.FrameCurve{}, false
		}
		im, err := fits.ReadImage(mergedFramePath(mergedDir, seqBase, idx))
		if err != nil {
			continue // soft-fail: probe the remaining frames
		}
		fc, ok := photom.MeasureImageMasked(im, overlapKeepFn(review.FrameH[idx], overlap, cv, gridW, gridH))
		if !ok {
			continue
		}
		curves = append(curves, fc)
	}
	if len(curves) == 0 {
		return photom.FrameCurve{}, false
	}
	return photom.MedianCurve(curves), true
}

// overlapKeepFn maps a frame pixel through the frame's homography onto the canvas grid and tests
// the overlap mask.
func overlapKeepFn(hm [9]float64, mask []bool, cv canvasSpec, gridW, gridH int) func(x, y int) bool {
	return func(x, y int) bool {
		cx, cy, ok := applyH3(hm, float64(x)+0.5, float64(y)+0.5)
		if !ok {
			return false
		}
		gx := int(cx+cv.OffX) / coverageDownscale
		gy := int(cy+cv.OffY) / coverageDownscale
		if gx < 0 || gy < 0 || gx >= gridW || gy >= gridH {
			return false
		}
		return mask[gy*gridW+gx]
	}
}

// addOffsetToFrames rewrites each frame in place as p += delta (header/WCS preserved).
func addOffsetToFrames(ctx context.Context, mergedDir, seqBase string, indices []int, delta float64) error {
	d := float32(delta)
	for _, idx := range indices {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := mergedFramePath(mergedDir, seqBase, idx)
		im, err := fits.ReadImage(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		for c := range im.Pix {
			plane := im.Pix[c]
			for i := range plane {
				plane[i] += d
			}
		}
		if err := im.OverwriteData(path); err != nil {
			return fmt.Errorf("rewrite %s: %w", path, err)
		}
	}
	return nil
}

// mergedFramePath names the merged sequence's frame file for a 0-based merged-order index.
func mergedFramePath(mergedDir, seqBase string, idx int) string {
	return filepath.Join(mergedDir, fmt.Sprintf("%s_%05d.fits", seqBase, idx+1))
}

// keptIndices lists the span's merged-order indices that survived the registration review.
func keptIndices(review registrationReview, span groupSpan) []int {
	var out []int
	for i := span.Start; i < span.End; i++ {
		if _, ok := review.FrameH[i]; ok {
			out = append(out, i)
		}
	}
	return out
}

// evenSpacedInts returns at most n evenly-spaced elements of idxs.
func evenSpacedInts(idxs []int, n int) []int {
	if n <= 0 || len(idxs) <= n {
		return idxs
	}
	step := len(idxs) / n
	out := make([]int, 0, n)
	for i := 0; i < len(idxs) && len(out) < n; i += step {
		out = append(out, idxs[i])
	}
	return out
}

// intersectWithFrac ANDs two same-length masks and reports the intersection's fraction of b's area.
func intersectWithFrac(a, b []bool) ([]bool, float64) {
	if len(a) != len(b) {
		return nil, 0
	}
	out := make([]bool, len(a))
	inter, area := 0, 0
	for i := range a {
		if b[i] {
			area++
		}
		if a[i] && b[i] {
			out[i] = true
			inter++
		}
	}
	if area == 0 {
		return out, 0
	}
	return out, float64(inter) / float64(area)
}

// seamOffsetLine renders one group's refit outcome for the live journal.
func seamOffsetLine(rec SeamOffset) string {
	if rec.Applied {
		return fmt.Sprintf("· seam offset %+.4g applied", rec.Delta)
	}
	return "· seam offset skipped: " + rec.Reason
}
