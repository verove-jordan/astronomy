package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// limb.go fits the solar limb. It is the load-bearing primitive of the whole mode: the fitted
// centre and radius are what register frames against each other, what normalise an afocal phone
// capture's unknown scale, what group files by configuration, and what every later step (limb
// darkening, prominence masking, disc cropping) is defined against.
//
// It exists rather than reusing planetary's lunar fit for two reasons that both showed up
// immediately on real data. The lunar fit is tuned around a crescent — it keeps only the sharpest
// 60% of boundary points to reject the terminator, which on the Sun instead discards whichever
// azimuth the etalon's sweet spot leaves dimmest, biasing the circle. And it bounds how far the
// centre may lie outside the frame, which rejects a limb close-up outright even though a long limb
// arc constrains a circle perfectly well. Both are right for the Moon and wrong here.
//
// The fit is two-stage: an algebraic circle through thresholded boundary points to get close, then
// a sub-pixel refinement that finds the limb along radial rays. The refinement is what earns the
// precision the registration needs — a 0.1% radius error is half a pixel of smear at the limb, in
// every frame.

const (
	// limbAzimuths is how many radial rays the sub-pixel stage samples. 720 gives half-degree
	// coverage, which is plenty to average down noise without costing real time.
	limbAzimuths = 720
	// limbSearchSpan is how far either side of the coarse radius a ray looks, as a fraction of it.
	limbSearchSpan = 0.08
	// limbSearchStep is the radial sampling step in pixels; the crossing is then interpolated.
	limbSearchStep = 0.25
	// borderGuard is how many pixels from the frame edge boundary points are ignored. The frame's
	// own cut edge is not a limb, and it is straight, so including it wrecks the fit.
	borderGuard = 3
	// limbTrimSigma is the robust-trim threshold, in MADs, for discarding non-limb points.
	limbTrimSigma = 3.0
	// limbArcBins counts azimuthal coverage in 5° bins.
	limbArcBins = 72
	// minLimbArcBins is the coverage below which a circle is not meaningfully constrained (60°).
	minLimbArcBins = 12
	// minLimbPoints is the fewest surviving points a fit may rest on.
	minLimbPoints = 40
	// minSkyContrast is how much darker the sky must be than the disc, as a fraction of the disc
	// level, for a limb to be present at all.
	minSkyContrast = 0.25
	// maxDiscFillForLimb is the largest fraction of the frame the disc may cover and still leave a
	// limb to fit.
	maxDiscFillForLimb = 0.97
)

// Limb is a fitted solar limb, in the pixel coordinates of the image it was fitted on.
type Limb struct {
	CX       float64 `json:"cx"`
	CY       float64 `json:"cy"`
	R        float64 `json:"r"`
	ArcDeg   float64 `json:"arc_deg"`   // how much of the limb was visible and voted
	ResidRMS float64 `json:"resid_rms"` // RMS radial residual of the surviving points, px
	Points   int     `json:"points"`
	Partial  bool    `json:"partial"` // the disc runs past the frame edge
}

// FitLimb fits the solar limb on the image's first plane, which must be in linear light.
// ok=false means no limb could be constrained — the caller skips the frame rather than guessing.
func FitLimb(im *fits.Image) (Limb, bool) {
	coarse, partial, ok := coarseLimb(im)
	if !ok {
		return Limb{}, false
	}
	fine, ok := refineLimb(im, coarse)
	if !ok {
		// The coarse fit already satisfied the coverage and point-count gates; keep it rather than
		// discarding a usable frame because the sub-pixel pass found the limb too soft to trace.
		coarse.Partial = partial
		return coarse, true
	}
	fine.Partial = partial
	return fine, true
}

// coarseLimb thresholds the disc, walks the boundary of its largest component and fits an algebraic
// circle, trimming outliers robustly.
func coarseLimb(im *fits.Image) (Limb, bool, bool) {
	mask, ok := discMask(im)
	if !ok {
		return Limb{}, false, false
	}
	pts, partial := boundaryPoints(mask, im.W, im.H)
	if len(pts) < minLimbPoints {
		return Limb{}, partial, false
	}
	c, ok := kasaCircle(pts)
	if !ok {
		return Limb{}, partial, false
	}
	for i := 0; i < 3; i++ {
		kept := trimByRadius(pts, c)
		if len(kept) < minLimbPoints {
			break
		}
		next, ok := kasaCircle(kept)
		if !ok {
			break
		}
		pts, c = kept, next
	}
	return finishLimb(c, pts), partial, acceptLimb(c, pts, im)
}

// discLevels measures the frame's sky and disc levels — the two ends the limb threshold sits
// between. It is a function of its own because the two-body fit needs the same pair of numbers to
// judge how much local contrast counts as a real edge (pair.go).
//
// Two passes: split roughly, then take the median of the bright side as the true disc level so the
// half-max threshold does not drift with how much of the frame the disc happens to fill.
func discLevels(im *fits.Image) (sky, disc float64, ok bool) {
	sample := imgops.Subsample(im.Pix[0], 200000)
	sky = imgops.Percentile(sample, 20)
	peak := imgops.Percentile(sample, 99.5)
	if peak-sky < 1e-9 {
		return 0, 0, false
	}
	rough := float32(sky + 0.5*(peak-sky))
	var bright []float32
	for _, v := range sample {
		if v > rough {
			bright = append(bright, v)
		}
	}
	if len(bright) < 16 {
		return 0, 0, false
	}
	disc = imgops.Percentile(bright, 50)
	// A limb only exists if there is sky to see it against. Without this gate a frame zoomed right
	// into the disc surface — no sky anywhere in it — still splits at the median of its own noise,
	// producing a speckled mask whose ragged boundary fits a plausible-looking circle. That circle
	// would then define the scale for its whole group. Refusing is the only honest answer.
	if disc <= 0 || (disc-sky)/disc < minSkyContrast {
		return 0, 0, false
	}
	return sky, disc, true
}

// discMask thresholds the frame at the half-way level between sky and disc — which is where the
// limb physically sits — and keeps the largest connected component.
func discMask(im *fits.Image) ([]bool, bool) {
	p := im.Pix[0]
	sky, disc, ok := discLevels(im)
	if !ok {
		return nil, false
	}
	thr := float32(sky + 0.5*(disc-sky))

	mask := make([]bool, len(p))
	on := 0
	for i, v := range p {
		mask[i] = v > thr
		if mask[i] {
			on++
		}
	}
	// Likewise if the "disc" fills essentially the whole frame: whatever boundary is left belongs to
	// the frame, not to the Sun.
	if float64(on) > maxDiscFillForLimb*float64(len(p)) {
		return nil, false
	}
	labels, n := imgops.Label(mask, im.W, im.H)
	if n == 0 {
		return nil, false
	}
	counts := make([]int, n+1)
	for _, l := range labels {
		if l > 0 {
			counts[l]++
		}
	}
	best := 1
	for l := 2; l <= n; l++ {
		if counts[l] > counts[best] {
			best = l
		}
	}
	out := make([]bool, len(mask))
	for i, l := range labels {
		out[i] = l == best
	}
	return out, true
}

// point is a candidate limb location.
type point struct{ x, y float64 }

// boundaryPoints returns the mask pixels that border the sky, skipping anything within borderGuard
// of the frame edge. partial reports whether the component reached the edge, i.e. the disc is cut.
func boundaryPoints(mask []bool, w, h int) (pts []point, partial bool) {
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if !mask[i] {
				continue
			}
			if x <= borderGuard || y <= borderGuard || x >= w-1-borderGuard || y >= h-1-borderGuard {
				partial = true
				continue
			}
			if !mask[i-1] || !mask[i+1] || !mask[i-w] || !mask[i+w] {
				pts = append(pts, point{float64(x), float64(y)})
			}
		}
	}
	return pts, partial
}

// kasaCircle is the algebraic (Kåsa) circle fit: minimise Σ(x²+y² + ax + by + c)² over a, b, c,
// which is linear and therefore has a closed form and no starting guess.
func kasaCircle(pts []point) (Limb, bool) {
	n := float64(len(pts))
	if n < 3 {
		return Limb{}, false
	}
	var sx, sy, sxx, syy, sxy, sz, sxz, syz float64
	for _, p := range pts {
		z := p.x*p.x + p.y*p.y
		sx += p.x
		sy += p.y
		sxx += p.x * p.x
		syy += p.y * p.y
		sxy += p.x * p.y
		sz += z
		sxz += p.x * z
		syz += p.y * z
	}
	m := [3][4]float64{
		{sxx, sxy, sx, -sxz},
		{sxy, syy, sy, -syz},
		{sx, sy, n, -sz},
	}
	sol, ok := solve3(m)
	if !ok {
		return Limb{}, false
	}
	cx, cy := -sol[0]/2, -sol[1]/2
	rr := cx*cx + cy*cy - sol[2]
	if rr <= 0 {
		return Limb{}, false
	}
	return Limb{CX: cx, CY: cy, R: math.Sqrt(rr)}, true
}

// solve3 solves a 3×3 system by Gaussian elimination with partial pivoting.
func solve3(m [3][4]float64) ([3]float64, bool) {
	for col := 0; col < 3; col++ {
		piv := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[piv][col]) {
				piv = r
			}
		}
		if math.Abs(m[piv][col]) < 1e-12 {
			return [3]float64{}, false
		}
		m[col], m[piv] = m[piv], m[col]
		for r := 0; r < 3; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for c := col; c < 4; c++ {
				m[r][c] -= f * m[col][c]
			}
		}
	}
	return [3]float64{m[0][3] / m[0][0], m[1][3] / m[1][1], m[2][3] / m[2][2]}, true
}

// trimByRadius keeps the points whose distance from the fitted centre is within limbTrimSigma MADs
// of the fitted radius — the robust way to shed prominences, sunspot-darkened edges and stray blobs.
func trimByRadius(pts []point, c Limb) []point {
	dev := make([]float64, len(pts))
	for i, p := range pts {
		dev[i] = math.Abs(math.Hypot(p.x-c.CX, p.y-c.CY) - c.R)
	}
	mad := median(dev)
	if mad <= 1e-9 {
		return pts
	}
	lim := limbTrimSigma * 1.4826 * mad
	kept := make([]point, 0, len(pts))
	for i, p := range pts {
		if dev[i] <= lim {
			kept = append(kept, p)
		}
	}
	return kept
}

// refineLimb re-locates the limb sub-pixel along radial rays, then refits.
//
// Each ray's limb is taken at the profile's INFLECTION POINT — the radius of steepest fall — not at
// a half-way brightness threshold. That choice is what makes the radius unbiased. A threshold has
// to be stated relative to some "disc level", and on a limb-darkened disc there is no single such
// level: the surface is still dimming as it approaches the limb, so any level measured a little way
// inside sits above the true edge brightness and the crossing lands inside the limb. On these
// fixtures that bias is about a pixel at ordinary limb darkening and grows to fifteen at strong
// darkening — an error that would go straight into every registered frame's scale.
//
// The steepest-gradient point has no such dependence: for a symmetric PSF it sits on the true edge
// regardless of how bright either side is, which also makes it immune to the etalon's sweet-spot
// gradient and to exposure changing between frames.
func refineLimb(im *fits.Image, coarse Limb) (Limb, bool) {
	pts := make([]point, 0, limbAzimuths)
	for i := 0; i < limbAzimuths; i++ {
		a := 2 * math.Pi * float64(i) / limbAzimuths
		cos, sin := math.Cos(a), math.Sin(a)
		if r, ok := limbByInflection(im, coarse, cos, sin); ok {
			pts = append(pts, point{coarse.CX + r*cos, coarse.CY + r*sin})
		}
	}
	if len(pts) < minLimbPoints {
		return Limb{}, false
	}
	c, ok := kasaCircle(pts)
	if !ok {
		return Limb{}, false
	}
	if kept := trimByRadius(pts, c); len(kept) >= minLimbPoints {
		if next, ok := kasaCircle(kept); ok {
			pts, c = kept, next
		}
	}
	out := finishLimb(c, pts)
	return out, out.ArcDeg >= float64(minLimbArcBins)*360/limbArcBins
}

// limbByInflection samples the radial profile across the limb and returns the radius of steepest
// fall, interpolated to sub-pixel by fitting a parabola to the gradient's peak.
func limbByInflection(im *fits.Image, c Limb, cos, sin float64) (float64, bool) {
	return edgeByInflection(im, c, cos, sin, edgeFalling, limbSearchSpan)
}

// edgeDirection is which way the brightness steps as a ray leaves the centre.
type edgeDirection float64

const (
	// edgeFalling is bright inside, dark outside — the solar limb against the sky.
	edgeFalling edgeDirection = 1
	// edgeRising is dark inside, bright outside — the lunar limb, whose interior is the occulting
	// body and whose exterior is the still-visible Sun. An eclipse is the only place this occurs,
	// and it is the whole reason this parameter exists: searching for a falling edge along a ray
	// that leaves the Moon's centre finds the far side of the crescent, tens of pixels away.
	edgeRising edgeDirection = -1
)

// edgeByInflection locates a step edge along one ray at its point of steepest change.
//
// The direction argument only flips which sign of gradient is sought; the sub-pixel vertex is the
// same parabola either way, because a parabola through three gradient samples has its extremum at
// the same place whether that extremum is a peak or a trough.
func edgeByInflection(im *fits.Image, c Limb, cos, sin float64, dir edgeDirection, span float64) (float64, bool) {
	r0, r1 := c.R*(1-span), c.R*(1+span)
	n := int((r1-r0)/limbSearchStep) + 1
	if n < 9 {
		return 0, false
	}
	prof := make([]float64, 0, n)
	for k := 0; k < n; k++ {
		r := r0 + float64(k)*limbSearchStep
		x, y := c.CX+r*cos, c.CY+r*sin
		if x < 1 || y < 1 || x >= float64(im.W-2) || y >= float64(im.H-2) {
			return 0, false // the ray leaves the frame: this azimuth has no measurable limb
		}
		prof = append(prof, float64(imgops.SampleCubic(im.Pix[0], im.W, im.H, x, y)))
	}
	smoothProfile(prof)

	// Central differences, then the most negative one — the outward-falling edge. A rising edge is
	// the same search on the negated profile.
	s := float64(dir)
	best, bestSlope := -1, 0.0
	for k := 1; k < len(prof)-1; k++ {
		if d := s * (prof[k+1] - prof[k-1]); d < bestSlope {
			best, bestSlope = k, d
		}
	}
	if best <= 1 || best >= len(prof)-2 || bestSlope >= 0 {
		return 0, false
	}
	// Parabolic vertex through the three gradient samples around the peak.
	gm := s * (prof[best] - prof[best-2])
	g0 := s * (prof[best+1] - prof[best-1])
	gp := s * (prof[best+2] - prof[best])
	shift := 0.0
	if den := gm - 2*g0 + gp; math.Abs(den) > 1e-12 {
		shift = 0.5 * (gm - gp) / den
	}
	if math.Abs(shift) > 1 {
		shift = 0
	}
	return r0 + (float64(best)+shift)*limbSearchStep, true
}

// smoothProfile applies a 5-tap binomial smooth in place. Differentiating raw samples would let
// noise pick the peak; over 720 rays that would show up as a radius that wanders frame to frame,
// which is the one thing registration cannot tolerate.
//
// Past either end the profile is continued by REFLECTING ABOUT THE ENDPOINT'S VALUE, which is what
// makes this usable on the two monotone profiles that also depend on it — limb darkening and the
// off-limb background. An odd extension preserves a straight line exactly, so a profile still falling
// steeply at its boundary comes through with that trend intact.
//
// The two obvious alternatives both corrupt exactly the bin that matters most. Leaving the ends
// untouched — which is what this did — smooths bin 2 against two raw neighbours on a profile falling
// an order of magnitude per bin, roughly doubling it, and leaves bins 0 and 1 unsmoothed beside it:
// a step. A plain mirror (src[-1] = src[1]) is worse still, folding the profile back on itself so the
// endpoint is dragged towards its own interior — and on the off-limb background, dragging the
// endpoint DOWN means under-subtracting the limb skirt, which the prominence stretch then renders as
// a ring.
func smoothProfile(v []float64) {
	n := len(v)
	if n < 5 {
		return
	}
	src := append([]float64(nil), v...)
	at := func(i int) float64 {
		switch {
		case i < 0:
			return 2*src[0] - src[clampInt(-i, 0, n-1)]
		case i >= n:
			return 2*src[n-1] - src[clampInt(2*(n-1)-i, 0, n-1)]
		}
		return src[i]
	}
	for i := range v {
		v[i] = (at(i-2) + 4*at(i-1) + 6*src[i] + 4*at(i+1) + at(i+2)) / 16
	}
}

// finishLimb fills the coverage and residual figures for a fitted circle.
func finishLimb(c Limb, pts []point) Limb {
	bins := make([]bool, limbArcBins)
	var sum float64
	for _, p := range pts {
		d := math.Hypot(p.x-c.CX, p.y-c.CY) - c.R
		sum += d * d
		a := math.Atan2(p.y-c.CY, p.x-c.CX)
		b := int((a + math.Pi) / (2 * math.Pi) * limbArcBins)
		bins[clampInt(b, 0, limbArcBins-1)] = true
	}
	covered := 0
	for _, b := range bins {
		if b {
			covered++
		}
	}
	c.Points = len(pts)
	c.ArcDeg = float64(covered) * 360 / limbArcBins
	c.ResidRMS = math.Sqrt(sum / float64(len(pts)))
	return c
}

// acceptLimb is the confidence gate: enough surviving points, enough of the circumference, and a
// radius that is physically plausible for the frame it was found in.
func acceptLimb(c Limb, pts []point, im *fits.Image) bool {
	if len(pts) < minLimbPoints || c.R <= 4 {
		return false
	}
	if c.R > 20*float64(max(im.W, im.H)) {
		return false
	}
	l := finishLimb(c, pts)
	return l.ArcDeg >= float64(minLimbArcBins)*360/limbArcBins
}
