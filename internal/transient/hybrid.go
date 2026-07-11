package transient

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// Line-validation tuning. A REAL satellite/plane trail sits in ONE frame's residual; fixed-pattern
// walking noise (uncalibrated darks) sits in EVERY frame — that is the discriminator that lets the line
// pass paint a trail's faint sub-threshold wings without hallucinating on walking noise (the reason the
// unvalidated line mask was reverted after it painted whole percents of a frame).
const (
	// maxLineSegments is trail.DetectSegments' own per-plane ceiling: when a single frame saturates it
	// (this many confirmed Hough lines in one sub), the frame is noise-dominated — real deep-sky subs
	// carry 0–1 trails — so its line pass is skipped and only the blanket + fixed-pattern-validated
	// passes touch it. The primary discriminator is still cross-frame uniqueness below.
	maxLineSegments      = 4
	lineFixedPatternFrac = 0.30 // corridor also elevated in ≥ this fraction of the OTHER frames ⇒ fixed pattern
	lineWingK            = 0.5  // corridor significance uses half the per-pixel k, to catch the faint wings
	minCorridorPx        = 24   // fewer corridor pixels than this ⇒ not a real trail extent
	skySubsampleMax      = 200000
)

// MaskCrossFrameValidated masks satellite/aircraft trails from a set of REGISTERED frames (mutated in
// place) with two passes: (1) the blanket per-pixel positive-outlier replacement (cosmic rays, hot
// pixels, and a trail's bright core — exactly MaskCrossFrame's behaviour), then (2) a LINE pass that
// paints the faint sub-threshold WINGS a per-pixel threshold misses, but ONLY for segments that survive
// a strict fixed-pattern test — significant over their corridor in their own frame AND elevated in fewer
// than lineFixedPatternFrac of the other frames (a real trail is unique to one frame; walking noise is
// not). A frame yielding more than maxLineSegments candidates has its line pass skipped (hallucination
// guard). Geostationary streaks (present in the cross-frame median) are repaired from local background.
// Fewer than MinFrames frames or k ≤ 0 → no-op. The Report carries per-frame + aggregate counts.
func MaskCrossFrameValidated(frames []*fits.Image, k float64) (*Report, error) {
	n := len(frames)
	if n < MinFrames || k <= 0 {
		return &Report{}, nil
	}
	w, h, c := frames[0].W, frames[0].H, frames[0].C
	if err := checkDims(frames, w, h, c); err != nil {
		return nil, err
	}
	med, sig := medianMADPlanes(frames, w, h, c)
	resid := make([][]float32, n)
	sky := make([]float64, n)
	for i, f := range frames {
		resid[i] = residualPlane(f, med, w, h, c)
		sky[i] = residualSky(resid[i])
	}

	rep := &Report{width: w, height: h}
	for fi, f := range frames {
		fr := FrameReport{Index: fi + 1}
		fr.MaskedPx += blanketMask(f, med, sig, k, w, h, c) // pass 1: per-pixel outliers (unchanged)
		maskFrameLines(f, med, resid, sky, fi, k, w, h, c, &fr, rep)
		rep.PerFrame = append(rep.PerFrame, fr)
	}
	maskGeostationary(frames, med, w, h, c, rep)
	return rep, nil
}

// maskFrameLines runs the validated line pass for frame fi: detect candidate segments on its residual,
// bail out (hallucination guard) if there are too many, else paint each segment that passes validTrail.
func maskFrameLines(f *fits.Image, med [][]float32, resid [][]float32, sky []float64, fi int,
	k float64, w, h, c int, fr *FrameReport, rep *Report) {
	segs := trail.DetectSegments(resid[fi], w, h, trail.DefaultParams(k))
	if len(segs) >= maxLineSegments {
		rep.skipped++ // detector saturated ⇒ noise-dominated frame; the blanket pass already cleaned real outliers
		return
	}
	for _, s := range segs {
		if !validTrail(s, resid, sky, fi, w, h) {
			rep.rejected++
			continue
		}
		fr.Segments++
		for ch := 0; ch < c; ch++ {
			fr.MaskedPx += trail.ApplySwathMedian(f.Pix[ch], med[ch], w, h, s)
		}
	}
}

// maskGeostationary repairs streaks that sit on the same sky pixels in a majority of frames (so they
// survive into the cross-frame median) from local background in every frame.
func maskGeostationary(frames []*fits.Image, med [][]float32, w, h, c int, rep *Report) {
	medSegs := detectMedianTrails(med, w, h)
	rep.geo = len(medSegs)
	for si, s := range medSegs {
		for fi, f := range frames {
			for ch := 0; ch < c; ch++ {
				rep.PerFrame[fi].MaskedPx += trail.ApplySwathLocalBG(f.Pix[ch], w, h, s, seedFor(si, ch))
			}
		}
	}
}

// validTrail accepts a segment only when it looks like a real one-frame trail: significant over its
// corridor in frame fi's residual (at half the per-pixel k, to include the faint wings) AND elevated in
// fewer than lineFixedPatternFrac of the OTHER frames' residuals (a trail is unique to one frame; walking
// noise / a faint galaxy edge repeats in every frame).
func validTrail(s trail.Segment, resid [][]float32, sky []float64, fi, w, h int) bool {
	idx := corridorIndices(s, w, h)
	if len(idx) < minCorridorPx {
		return false
	}
	if corridorMean(resid[fi], idx) < lineWingK*sky[fi] {
		return false // not even significant in its own frame
	}
	elevated := 0
	for j := range resid {
		if j == fi {
			continue
		}
		if corridorMean(resid[j], idx) >= lineWingK*sky[j] {
			elevated++
		}
	}
	return float64(elevated)/float64(len(resid)-1) < lineFixedPatternFrac
}

// corridorIndices returns the unique pixel indices inside segment s's masking swath, walking ALONG the
// line (t ∈ [T0,T1]) and stepping ⊥ up to Width each side — matching Segment.Contains without scanning
// the whole plane.
func corridorIndices(s trail.Segment, w, h int) []int {
	seen := map[int]struct{}{}
	var idx []int
	p0x, p0y := s.Nx*s.C, s.Ny*s.C
	for t := s.T0; t <= s.T1; t++ {
		bx, by := p0x-t*s.Ny, p0y+t*s.Nx
		for u := -s.Width; u <= s.Width; u++ {
			x, y := int(math.Round(bx+u*s.Nx)), int(math.Round(by+u*s.Ny))
			if x < 0 || x >= w || y < 0 || y >= h {
				continue
			}
			i := y*w + x
			if _, ok := seen[i]; !ok {
				seen[i] = struct{}{}
				idx = append(idx, i)
			}
		}
	}
	return idx
}

// corridorMean averages a residual plane over the precomputed corridor indices.
func corridorMean(plane []float32, idx []int) float64 {
	if len(idx) == 0 {
		return 0
	}
	var sum float64
	for _, i := range idx {
		sum += float64(plane[i])
	}
	return sum / float64(len(idx))
}

// residualSky estimates a residual plane's robust sky noise (1.4826·MAD) on a capped subsample — the
// trail is a tiny fraction of the plane, so the subsample MAD tracks the sky, not the streak.
func residualSky(plane []float32) float64 {
	step := 1
	if len(plane) > skySubsampleMax {
		step = len(plane) / skySubsampleMax
	}
	sub := make([]float64, 0, len(plane)/step+1)
	for i := 0; i < len(plane); i += step {
		sub = append(sub, float64(plane[i]))
	}
	m := median(sub)
	for i := range sub {
		sub[i] = math.Abs(sub[i] - m)
	}
	sort.Float64s(sub)
	return 1.4826 * sub[len(sub)/2]
}
