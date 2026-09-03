package pipeline

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/grade"
)

// Cross-night registration review: between the merged 2-pass register and its application, the
// per-frame homographies are inspected in Go. The anchor group's reference frame is pinned
// (setref) so the applied canvas (framing=current) is the anchor night's full field; frames whose
// transform is physically absurd for a same-target session are excluded from the stack; and each
// group's median field rotation/overlap is surfaced — the visible answer to "did the cross-night
// star match actually work?".

// absurdOverlapFrac rejects a registered frame whose transformed footprint overlaps the anchor
// canvas by less than this fraction. Same-target sessions share most of the field (a 140°
// night-to-night rotation still overlaps ≥50%), so lower means a false star match: stacked it
// would only add noise — and under the old framing=min a single such frame collapsed the whole
// channel master to the intersection sliver (task #312).
const absurdOverlapFrac = 0.20

// groupSpan locates one group's frames inside the merged sequence: [Start, End), 0-based, in the
// exact order the calibrated frames were linked.
type groupSpan struct{ Start, End int }

// registrationReview is the outcome of inspecting a merged 2-pass registration.
type registrationReview struct {
	RefIndex  int            // 1-based sequence index to pin as reference; 0 = keep Siril's choice
	PreReject map[int]string // 0-based frame index → stack-exclusion reason (absurd transforms)
	Groups    []groupReview  // parallel to the reviewed spans
	Warnings  []string
	// FrameH keeps each kept frame's homography RELATIVE to the anchor reference (frame → canvas)
	// — the raw material of the coverage rasterization. Captured here because the merged .seq is
	// deleted with its work dir after stacking.
	FrameH map[int][9]float64
}

// groupReview is one group's registration telemetry.
type groupReview struct {
	Registered  int
	Absurd      int
	RotationDeg *float64 // median field rotation vs the anchor canvas (degrees)
	OverlapFrac *float64 // median footprint overlap with the anchor canvas (0..1)
}

// reviewMergedRegistration parses the merged sequence's registration data and derives the anchor
// reference, the absurd-transform rejections and the per-group telemetry. w/h are the calibrated
// frame dimensions (all frames of a linkable sequence share them). Soft contract: on a sequence
// with no usable homographies it returns a zero review with a warning, never an error the caller
// must abort on — Siril's own register/apply failures surface through the runner instead.
func reviewMergedRegistration(seqPath string, spans []groupSpan, anchor int, w, h int) registrationReview {
	review := registrationReview{PreReject: map[int]string{}, Groups: make([]groupReview, len(spans)),
		FrameH: map[int][9]float64{}}
	seq, err := grade.ParseSeq(seqPath)
	if err != nil {
		review.Warnings = append(review.Warnings, "registration review skipped: "+err.Error())
		return review
	}
	registered := func(i int) bool {
		return i >= 0 && i < len(seq.Metrics) && seq.Metrics[i].FWHM > 0 && seq.Metrics[i].HasH
	}

	refIdx := -1
	if anchor >= 0 && anchor < len(spans) {
		refIdx = pickAnchorRef(spans[anchor], registered)
	}
	if refIdx >= 0 {
		review.RefIndex = refIdx + 1 // Siril setref is 1-based
	} else {
		review.Warnings = append(review.Warnings,
			"no anchor-night frame registered — keeping Siril's own reference frame")
		if refIdx = seq.Reference; !registered(refIdx) {
			return review // nothing to measure against; grading still applies its own rules
		}
	}
	if w <= 0 || h <= 0 {
		review.Warnings = append(review.Warnings, "frame dimensions unreadable — transform review skipped")
		return review
	}
	reviewTransforms(&review, seq, spans, refIdx, float64(w), float64(h))
	return review
}

// unionCanvasOf bounds every kept frame's transformed corners on the anchor-ref plane — the mosaic
// union canvas (dims + the anchor frame's origin offset), computed in Go so the padding step is
// exact (live-pinned by TestSirilLive_PaddedReregisterUnionCanvas: pad → re-register →
// framing=current reproduces exactly this canvas).
func (r *registrationReview) unionCanvasOf(w, h int) canvasSpec {
	minX, minY, maxX, maxY := 0.0, 0.0, float64(w), float64(h)
	for _, hm := range r.FrameH {
		quad, ok := frameQuad(hm, w, h, canvasSpec{W: w, H: h})
		if !ok {
			continue
		}
		for _, q := range quad {
			minX, maxX = math.Min(minX, q[0]), math.Max(maxX, q[0])
			minY, maxY = math.Min(minY, q[1]), math.Max(maxY, q[1])
		}
	}
	return canvasSpec{
		W:    int(math.Ceil(maxX)) - int(math.Floor(minX)),
		H:    int(math.Ceil(maxY)) - int(math.Floor(minY)),
		OffX: -math.Floor(minX),
		OffY: -math.Floor(minY),
	}
}

// frameDims reads a FITS frame's NAXIS1/NAXIS2 — (0,0) when unreadable, which makes the review
// skip its transform gate rather than mis-gate every frame against a zero canvas.
func frameDims(path string) (int, int) {
	f, err := fits.Open(path)
	if err != nil {
		return 0, 0
	}
	w, _ := f.Header.Int("NAXIS1")
	h, _ := f.Header.Int("NAXIS2")
	return int(w), int(h)
}

// frameChannels is a frame's plane count (NAXIS3), 1 when the keyword is absent.
//
// It is what turns a per-plane size into a per-FRAME size, and the two are not the same once a
// one-shot-colour sequence is debayered: fits.Image keeps C separate W*H float32 planes
// (Pix[channel]), so a calibrated OSC frame costs THREE times a mono one. Sizing a memory budget
// from W*H*4 alone under-counts an OSC run by 3× — see trailmask.go, where that killed the engine.
func frameChannels(path string) int {
	f, err := fits.Open(path)
	if err != nil {
		return 1
	}
	if c, ok := f.Header.Int("NAXIS3"); ok && c > 0 {
		return int(c)
	}
	return 1
}

// pickAnchorRef returns the anchor span's reference frame: the middle frame when it registered,
// else the nearest registered neighbour (middle-out) — or -1 when none registered.
func pickAnchorRef(span groupSpan, registered func(int) bool) int {
	if span.End <= span.Start {
		return -1
	}
	mid := span.Start + (span.End-span.Start)/2
	for off := 0; ; off++ {
		lo, hi := mid-off, mid+off
		if lo < span.Start && hi >= span.End {
			return -1
		}
		if hi < span.End && registered(hi) {
			return hi
		}
		if lo >= span.Start && registered(lo) {
			return lo
		}
	}
}

// reviewTransforms fills the per-frame rejections and per-group telemetry relative to frame
// refIdx's canvas.
func reviewTransforms(review *registrationReview, seq *grade.Sequence, spans []groupSpan, refIdx int, w, h float64) {
	refH := seq.Metrics[refIdx].H
	for gi, span := range spans {
		var rotations, overlaps []float64
		for i := span.Start; i < span.End && i < len(seq.Metrics); i++ {
			m := seq.Metrics[i]
			if m.FWHM <= 0 || !m.HasH {
				continue // Siril already dropped it; grading reports it
			}
			review.Groups[gi].Registered++
			rel, ok := grade.RelativeH(refH, m.H)
			if !ok {
				continue
			}
			overlap := grade.FootprintOverlap(rel, w, h)
			if overlap < absurdOverlapFrac {
				review.Groups[gi].Absurd++
				review.PreReject[i] = fmt.Sprintf(
					"absurd registration transform (%.0f%% overlap with the anchor field) — false star match", overlap*100)
				continue
			}
			rotations = append(rotations, grade.RotationDeg(rel))
			overlaps = append(overlaps, overlap)
			review.FrameH[i] = rel
		}
		if len(rotations) > 0 {
			rot, ovl := medianOf(rotations), medianOf(overlaps)
			review.Groups[gi].RotationDeg, review.Groups[gi].OverlapFrac = &rot, &ovl
		}
	}
}

// registrationLine renders one group's registration telemetry for the per-session journal.
func registrationLine(filter string, gr groupReview) string {
	if gr.RotationDeg == nil {
		return fmt.Sprintf("  · %s: no frames survived the transform review (%d absurd)", filter, gr.Absurd)
	}
	line := fmt.Sprintf("  · %s: field rotation %+.1f° vs anchor, overlap %.0f%%", filter, *gr.RotationDeg, *gr.OverlapFrac*100)
	if gr.Absurd > 0 {
		line += fmt.Sprintf(" (%d absurd transform(s) excluded)", gr.Absurd)
	}
	return line
}
