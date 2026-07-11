// Package dither diagnoses the capture-time pointing pattern of a registered light sequence from
// its per-frame registration offsets. The pattern decides what happens to the fixed-pattern noise
// residuals (warm/unstable pixels, calibration leftovers) that live at FIXED sensor positions
// while the sky moves:
//
//   - dithered (random offsets) — after alignment each residual lands somewhere else in every
//     frame, so stack rejection removes it entirely: the ideal;
//   - linear drift (unguided/imperfect tracking) — residuals march in a straight line and smear
//     into correlated "walking noise" streaks that rejection struggles with;
//   - static (tight tracking, no dithering) — residuals add coherently and never average out.
//
// The diagnostic is advisory: it cannot fix a session after the fact, but it tells the user the
// single highest-impact capture-side improvement (enable random dithering) with evidence.
package dither

import (
	"fmt"
	"math"
	"sort"
)

// Shift is one frame's registration translation vs the reference, in pixels, capture order.
type Shift struct {
	X float64
	Y float64
}

// Report classifies a sequence's pointing pattern.
type Report struct {
	// Pattern is "dithered", "drift", "static" or "mixed".
	Pattern string `json:"pattern"`
	// Frames is the number of registered frames the diagnosis used.
	Frames int `json:"frames"`
	// SpanPx is the robust extent of the offset cloud (5th–95th percentile diagonal).
	SpanPx float64 `json:"span_px"`
	// StepMedianPx is the median offset change between consecutive frames.
	StepMedianPx float64 `json:"step_median_px"`
	// DirectionR is the step-direction coherence in [0,1]: 1 = every step points the same way
	// (a straight drift line), ~0 = directions are random (dithering).
	DirectionR float64 `json:"direction_r"`
	// DriftPxPerFrame is the magnitude of the mean step vector — the systematic drift rate.
	DriftPxPerFrame float64 `json:"drift_px_per_frame"`
	// Note is the human advisory derived from the pattern.
	Note string `json:"note,omitempty"`
}

const (
	minFrames    = 5   // fewer offsets cannot support a pattern claim
	staticSpanPx = 2.0 // cloud smaller than this: pointing is effectively static
	lineR        = 0.8 // step coherence at/above this: a straight drift line
	randomR      = 0.5 // coherence at/below this (with real steps): random offsets
	ditherStepPx = 2.0 // median step at/above this counts as a deliberate offset
	minStepPx    = 0.3 // steps below this are registration jitter, not motion — excluded from R
	jumpFactor   = 10  // steps beyond jumpFactor×median (+jumpFloorPx) are session jumps, excluded
	jumpFloorPx  = 20
	spanLoQ      = 0.05
	spanHiQ      = 0.95
)

// Analyze classifies the pointing pattern of a sequence's registration offsets (capture order).
// It returns nil when there are too few frames to judge.
func Analyze(shifts []Shift) *Report {
	if len(shifts) < minFrames {
		return nil
	}
	r := &Report{Frames: len(shifts), SpanPx: span(shifts)}

	// Consecutive steps, with cross-session pointing jumps excluded: a merged multi-session
	// sequence has a huge one-off step at each session boundary that says nothing about dithering.
	steps := make([]Shift, 0, len(shifts)-1)
	mags := make([]float64, 0, len(shifts)-1)
	for i := 1; i < len(shifts); i++ {
		s := Shift{X: shifts[i].X - shifts[i-1].X, Y: shifts[i].Y - shifts[i-1].Y}
		steps = append(steps, s)
		mags = append(mags, math.Hypot(s.X, s.Y))
	}
	r.StepMedianPx = medianOf(append([]float64(nil), mags...))
	jumpCut := math.Max(jumpFloorPx, jumpFactor*r.StepMedianPx)

	// Direction coherence over the real (non-jitter, non-jump) steps, and the mean drift vector.
	var ux, uy, mx, my float64
	moving := 0
	kept := 0
	for i, s := range steps {
		if mags[i] > jumpCut {
			continue
		}
		mx += s.X
		my += s.Y
		kept++
		if mags[i] < minStepPx {
			continue
		}
		ux += s.X / mags[i]
		uy += s.Y / mags[i]
		moving++
	}
	if moving >= 3 {
		r.DirectionR = math.Hypot(ux, uy) / float64(moving)
	}
	if kept > 0 {
		r.DriftPxPerFrame = math.Hypot(mx/float64(kept), my/float64(kept))
	}

	r.classify()
	return r
}

func (r *Report) classify() {
	switch {
	case r.SpanPx < staticSpanPx:
		r.Pattern = "static"
		r.Note = fmt.Sprintf(
			"static pointing (offset span %.1f px over %d frames): fixed-pattern residuals stack coherently and never average out — enable random dithering (~10 px between subs)",
			r.SpanPx, r.Frames)
	case r.DirectionR >= lineR:
		r.Pattern = "drift"
		r.Note = fmt.Sprintf(
			"linear drift ≈ %.1f px/frame with no dithering (direction coherence %.2f): residual warm/unstable pixels smear into walking-noise streaks — enable random dithering (~10 px between subs)",
			r.DriftPxPerFrame, r.DirectionR)
	case r.StepMedianPx >= ditherStepPx && r.DirectionR <= randomR:
		r.Pattern = "dithered"
		r.Note = fmt.Sprintf(
			"dithered capture (median offset %.1f px, direction coherence %.2f): fixed-pattern residuals decorrelate and are removed by stack rejection",
			r.StepMedianPx, r.DirectionR)
	default:
		r.Pattern = "mixed"
		r.Note = fmt.Sprintf(
			"slow/irregular offsets (median step %.1f px, coherence %.2f, span %.1f px): only partial fixed-pattern decorrelation — explicit random dithering (~10 px between subs) would improve rejection",
			r.StepMedianPx, r.DirectionR, r.SpanPx)
	}
}

// WalkingNoiseRisk reports whether the pattern leaves fixed-pattern residuals correlated in the
// stack (the cases worth a run-level warning).
func (r *Report) WalkingNoiseRisk() bool {
	return r != nil && (r.Pattern == "drift" || r.Pattern == "static")
}

// span is the robust diagonal extent of the offset cloud: the 5th–95th percentile range per axis,
// combined. Percentiles keep one bad registration from inflating the span; with few frames the
// quantiles converge to min/max.
func span(shifts []Shift) float64 {
	xs := make([]float64, len(shifts))
	ys := make([]float64, len(shifts))
	for i, s := range shifts {
		xs[i], ys[i] = s.X, s.Y
	}
	return math.Hypot(quantileRange(xs, spanLoQ, spanHiQ), quantileRange(ys, spanLoQ, spanHiQ))
}

func quantileRange(v []float64, lo, hi float64) float64 {
	sort.Float64s(v)
	return quantileSorted(v, hi) - quantileSorted(v, lo)
}

func quantileSorted(v []float64, q float64) float64 {
	if len(v) == 0 {
		return 0
	}
	pos := q * float64(len(v)-1)
	i := int(pos)
	if i >= len(v)-1 {
		return v[len(v)-1]
	}
	frac := pos - float64(i)
	return v[i]*(1-frac) + v[i+1]*frac
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[len(v)/2]
}
