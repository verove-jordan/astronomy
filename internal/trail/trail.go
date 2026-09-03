// Package trail is line-aware satellite / aircraft-trail detection and masking for AstroStack. Unlike
// the per-frame flag in internal/grade (which only says "this sub has a trail"), it localises each
// straight streak as a Segment — an oriented line with a finite extent and a Gaussian width — so the
// pipeline can paint the streak out with local background instead of rejecting the whole frame.
//
// Detection ports the Hough math of grade.DetectTrail (identical tuning constants) but works on
// float32 planes, confirms the peak line with a connected-component test, and refines the line, width
// and endpoints at full resolution. Every entry point soft-fails: degenerate input yields nil / a
// no-op and nothing ever panics.
package trail

import "math"

// Mode selects the seed-threshold regime for DetectSegments.
type Mode int

const (
	// Residual is tuned for residual / median-subtracted planes whose background is flat and where a
	// trail stands out at a low multiple of the robust sigma.
	Residual Mode = iota
	// Raw is tuned for raw light frames full of stars; it seeds at the bright 5-sigma level so star
	// pixels do not flood the Hough accumulator.
	Raw
)

// Params configures a detection pass. K scales the seed threshold in Residual mode.
type Params struct {
	Mode Mode
	K    float64
	// VoteFrac overrides how much of the frame a line must span to be accepted, as a fraction of the
	// pooled grid's shorter side. 0 keeps trailVoteFrac.
	//
	// The default is tuned for SATELLITE trails, which cross the whole field. A meteor does not: on a
	// real panel they span roughly a tenth of the frame, so the default silently refuses every one of
	// them — the detector returns nothing at all rather than something poor. Lower it to hunt short
	// streaks, and expect more false lines for it.
	VoteFrac float64
	// RawSeedK overrides the Raw-mode seed threshold (median + k·sigma of the pooled grid). 0 keeps
	// trailBrightK.
	//
	// Raw mode deliberately IGNORES K — seedK returns the constant — so RawParams(k) takes a k and
	// discards it, and a caller sweeping k sees the identical answer every time. That is fine for the
	// satellite pass it was written for and useless for anything fainter or brighter, so the override
	// is a separate field: changing what K means in Raw mode would silently move every existing
	// caller.
	RawSeedK float64
}

// voteFrac is the span requirement this pass should use.
func (p Params) voteFrac() float64 {
	if p.VoteFrac > 0 {
		return p.VoteFrac
	}
	return trailVoteFrac
}

// DefaultParams returns Residual-mode parameters (median-subtracted planes).
func DefaultParams(k float64) Params { return Params{Mode: Residual, K: k} }

// RawParams returns Raw-mode parameters (undedimmed light frames).
func RawParams(k float64) Params { return Params{Mode: Raw, K: k} }

// Detector tuning — kept numerically identical to grade.DetectTrail so both agree.
const (
	trailBrightK   = 5.0  // Raw-mode seed threshold: median + k·(1.4826·MAD)
	trailThetaN    = 180  // Hough angular resolution (1° steps over [0,π))
	trailMaxBrite  = 0.30 // reject a pass if more than this fraction of pooled cells are bright
	trailVoteFrac  = 0.25 // the peak line must span at least this fraction of min(gw,gh)
	trailPeakRatio = 4.0  // the peak must dominate the 99th percentile of populated bins
	swathDilate    = 2.0  // Contains masks within dilate·Width/2 = Width of the line
)

// Segment is one detected trail: the infinite line n·p = C in full-resolution pixels (n = (Nx,Ny) is a
// unit normal), to be masked only over the extent t ∈ [T0,T1] measured along the line direction
// dir = (-Ny, Nx), with Gaussian full-width Width (px, FWHM). Score is the Hough span confidence
// (peak votes ÷ min pooled dimension).
type Segment struct {
	Nx, Ny, C, T0, T1, Width, Score float64
}

// dirVec returns the unit direction along the line (perpendicular to the normal).
func (s Segment) dirVec() (float64, float64) { return -s.Ny, s.Nx }

// perpDist returns the signed perpendicular distance of (x,y) from the line.
func (s Segment) perpDist(x, y float64) float64 { return s.Nx*x + s.Ny*y - s.C }

// project returns the coordinate of (x,y) along the line direction dir = (-Ny, Nx).
func (s Segment) project(x, y float64) float64 { return -s.Ny*x + s.Nx*y }

// Contains reports whether (x,y) lies inside the masking swath: within ±Width of the line (dilate
// factor 2.0 over the half-width Width/2) and between the extent endpoints.
func (s Segment) Contains(x, y float64) bool {
	if math.Abs(s.perpDist(x, y)) > swathDilate*s.Width/2 {
		return false
	}
	t := s.project(x, y)
	return t >= s.T0 && t <= s.T1
}

// borderT returns the two parameter values where the line exits the w×h image rectangle, in the same
// t-coordinate as project / T0 / T1. ok is false for a degenerate (near-parallel, no-crossing) line.
func borderT(s Segment, w, h int) (t0, t1 float64, ok bool) {
	dx, dy := s.dirVec()
	p0x, p0y := s.Nx*s.C, s.Ny*s.C // point on the line closest to the origin
	var ts []float64
	const eps = 1e-6
	add := func(t, x, y float64) {
		if x >= -eps && x <= float64(w)+eps && y >= -eps && y <= float64(h)+eps {
			ts = append(ts, t)
		}
	}
	if math.Abs(dx) > 1e-9 {
		for _, bx := range [2]float64{0, float64(w)} {
			t := (bx - p0x) / dx
			add(t, bx, p0y+t*dy)
		}
	}
	if math.Abs(dy) > 1e-9 {
		for _, by := range [2]float64{0, float64(h)} {
			t := (by - p0y) / dy
			add(t, p0x+t*dx, by)
		}
	}
	if len(ts) < 2 {
		return 0, 0, false
	}
	t0, t1 = ts[0], ts[0]
	for _, t := range ts {
		t0, t1 = math.Min(t0, t), math.Max(t1, t)
	}
	return t0, t1, true
}

// clampi clamps v to [lo,hi].
func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
