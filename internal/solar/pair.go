package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// pair.go fits the TWO-BODY geometry of an eclipsed Sun — the solar limb and the occulting lunar
// limb — as two circles measured from a single frame.
//
// It exists because every measurement in this package is defined against one circle, and on a
// crescent that assumption does not merely lose accuracy, it silently picks the wrong body. The
// visible boundary of a partially eclipsed Sun lies on TWO circles of near-equal radius: the solar
// limb on the outside and the lunar limb on the inside. Near maximum roughly half the boundary
// points belong to each. Handed that mixture, the algebraic fit in limb.go converges on a blend and
// the robust trim then keeps whichever population happens to win — which changes from frame to
// frame. Registration, scale, limb darkening and the deconvolution width are all defined against
// that circle, so a fit that flips is not a small error.
//
// THE SEPARATION IS MEASURED PER POINT, BEFORE ANY FITTING, and that ordering is the whole idea.
// The obvious approach — fit two circles by RANSAC and label them afterwards — is both slower and
// weaker, because near maximum the two circles are only about a crescent-width apart (twenty pixels
// against a three-hundred pixel radius on the 12 Aug 2026 clips), so a three-point sample spanning
// both edges still yields a plausible circle. But the two edges differ in a way that is local,
// unambiguous and needs no fitting at all: the brightness steps in OPPOSITE directions across them.
//
// Walking outward from anywhere near the disc centre, the solar limb goes bright→dark (Sun, then
// sky) while the lunar limb goes dark→bright (Moon, then the still-visible Sun). Both gradients
// point into the crescent, which is exactly why they are opposite: the crescent lies inside one
// edge and outside the other. Sampling a few pixels either side of each boundary point therefore
// classifies it outright, and the two circles are then fitted to points already known to belong to
// them.
//
// A frame with no occulting body classifies every boundary point as solar and reports Moon.R == 0,
// so a full disc goes through this code to exactly the same answer FitLimb would have given.

const (
	// pairProbePx is how far either side of a boundary point the bright-side test samples. It has to
	// clear the edge's own blur — a couple of PSF widths — without reaching across a thin crescent
	// to the other body. Three pixels sits comfortably between the two on these captures.
	pairProbePx = 3.0
	// pairMinEdgeContrast is the inner/outer difference a point must show, as a fraction of the
	// frame's own disc-to-sky contrast, before its direction is believed. It exists for the CUSPS,
	// where the crescent narrows to nothing and both probes land on the same few pixels: those
	// points carry no information about which body they belong to and voting them either way drags
	// the centre along the crescent's axis.
	pairMinEdgeContrast = 0.05
	// pairMoonRadiusLo/Hi bound the occulter's radius against the Sun's. The Moon's apparent
	// diameter runs from 0.945 to 1.055 of the Sun's over the year; the band here is wider than that
	// because the two radii are measured from arcs of different length and the looser one is still
	// far tighter than anything a spurious second circle would satisfy.
	pairMoonRadiusLo = 0.80
	pairMoonRadiusHi = 1.25
	// pairConsensusTries is how many three-point circles the occulter's consensus stage tries. With a
	// few percent contamination a clean triplet comes up almost immediately; this is sized for the
	// far worse sets a real frame full of filaments can produce.
	pairConsensusTries = 200
	// pairInlierPx is how close a point must sit to a candidate circle to vote for it. Wide enough to
	// hold the fit's own sub-pixel scatter, tight enough that a filament's outline never votes for the
	// occulter's circle.
	pairInlierPx = 2.0
	// pairOcculterMaxLevel is how bright an occulter's interior may be, as a fraction of the way from
	// sky to disc. An opaque body passes no light, so it should sit at the sky's level; a dark
	// filament — the thing most likely to be mistaken for one — bottoms out around 55% of the disc.
	// A quarter of the way up leaves ample room for scattered light and none for a filament.
	pairOcculterMaxLevel = 0.25
	// pairInteriorFrac is how far in from a circle's edge the interior is sampled.
	pairInteriorFrac = 0.6
	// pairMinArcDeg is the shortest arc a second circle may be fitted from. Below this the centre is
	// poorly constrained along the arc's own normal, and a badly-placed occulter is worse than none:
	// it would mask real Sun and expose real Moon.
	pairMinArcDeg = 40
)

// Pair is the geometry of an eclipsed Sun in the pixel coordinates of the frame it was fitted on.
// Moon.R == 0 means no occulting body was found, which is the ordinary full-disc case.
type Pair struct {
	Sun  Limb `json:"sun"`
	Moon Limb `json:"moon"`
	// Obscuration is the fraction of the solar disc the occulter covers, 0..1, computed from the
	// two fitted circles rather than from pixel counting so it does not move with the exposure.
	Obscuration float64 `json:"obscuration"`
}

// Eclipsed reports whether an occulting body was found and is worth honouring.
func (p Pair) Eclipsed() bool { return p.Moon.R > 0 }

// FitPair fits the solar limb and, when one is present, the occulting lunar limb.
//
// ok=false means no solar limb could be constrained — the caller skips the frame rather than
// guessing, exactly as it would for FitLimb.
func FitPair(im *fits.Image) (Pair, bool) {
	sky, disc, ok := discLevels(im)
	if !ok {
		return Pair{}, false
	}
	mask, ok := discMask(im)
	if !ok {
		return Pair{}, false
	}
	pts, partial := boundaryPoints(mask, im.W, im.H)
	if len(pts) < minLimbPoints {
		return Pair{}, false
	}
	// One circle through everything first. It is a blend of the two bodies and is never used as an
	// answer — only as the origin the outward direction is measured from, for which it is far more
	// than accurate enough: it lands within half the centre separation of both true centres, and
	// the direction it defines is being used at a radius an order of magnitude larger.
	blend, ok := kasaCircle(pts)
	if !ok {
		return Pair{}, false
	}

	sunPts, moonPts := splitByEdgeDirection(im, pts, blend, pairMinEdgeContrast*(disc-sky))
	if len(sunPts) < minLimbPoints {
		// No point stepped bright→dark: there is no solar limb in this frame to anchor anything on.
		return Pair{}, false
	}

	sun, ok := fitFrom(sunPts)
	if !ok || !acceptLimb(sun, sunPts, im) {
		return Pair{}, false
	}
	if fine, ok := refineAgainst(im, sun, sunPts, edgeFalling); ok {
		sun = fine
	}
	sun.Partial = partial

	out := Pair{Sun: sun}
	moon, ok := fitMoon(im, moonPts, sun, sky, disc)
	if !ok {
		return out, true
	}
	out.Moon = moon
	out.Obscuration = OverlapFraction(sun, moon)
	return out, true
}

// splitByEdgeDirection classifies each boundary point by which way the brightness steps across it,
// measured along the outward radial from a rough centre.
//
// minContrast is stated in the frame's own units and is what keeps the cusps out; a point whose two
// probes disagree by less than that is dropped rather than assigned.
func splitByEdgeDirection(im *fits.Image, pts []point, from Limb, minContrast float64) (sun, moon []point) {
	p := im.Pix[0]
	for _, q := range pts {
		dx, dy := q.x-from.CX, q.y-from.CY
		d := math.Hypot(dx, dy)
		if d < 1 {
			continue
		}
		ux, uy := dx/d, dy/d
		ix, iy := q.x-pairProbePx*ux, q.y-pairProbePx*uy
		ox, oy := q.x+pairProbePx*ux, q.y+pairProbePx*uy
		if !insideFrame(im, ix, iy) || !insideFrame(im, ox, oy) {
			continue
		}
		inner := float64(imgops.SampleCubic(p, im.W, im.H, ix, iy))
		outer := float64(imgops.SampleCubic(p, im.W, im.H, ox, oy))
		switch diff := inner - outer; {
		case diff > minContrast:
			sun = append(sun, q) // bright inside, dark outside: the Sun against the sky
		case -diff > minContrast:
			moon = append(moon, q) // dark inside, bright outside: the occulter against the Sun
		}
	}
	return sun, moon
}

// insideFrame reports whether a sample position leaves room for the cubic kernel.
func insideFrame(im *fits.Image, x, y float64) bool {
	return x >= 1 && y >= 1 && x < float64(im.W-2) && y < float64(im.H-2)
}

// fitFrom is the algebraic circle plus the robust trim, the same two steps coarseLimb takes once it
// has its points — here over points already known to belong to one body.
func fitFrom(pts []point) (Limb, bool) {
	c, ok := kasaCircle(pts)
	if !ok {
		return Limb{}, false
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
	return finishLimb(c, pts), true
}

// fitRobust is fitFrom with a consensus stage in front of it, for a point set that may contain
// points belonging to something other than the body being fitted.
//
// The occulter's point set needs this and the Sun's does not, and the asymmetry is one of scale. Both
// are contaminated by the same thing — a dark FILAMENT on the disc dips below the mask threshold, and
// the outline of the resulting hole contributes points to both classes, bright-inside on its near
// edge and dark-inside on its far one. The Sun is bounded by a couple of thousand points and shrugs
// a dozen off. The occulter, whose visible arc is short when the eclipse is shallow, is not:
// MEASURED on a fixture at 40% obscuration, SEVENTEEN spurious points out of 436 moved the fitted
// radius from 1.03 of the Sun's to 0.75 and its residual from 0.31 px to 39.92.
//
// A least-squares circle cannot defend itself here — every point pulls on it — and the robust trim
// that follows cannot rescue it either, because it measures deviations about the circle that has
// already been dragged away. Consensus first, least squares second, is the only order that works.
func fitRobust(pts []point) (Limb, bool) {
	if len(pts) < minLimbPoints {
		return Limb{}, false
	}
	// A fixed sequence rather than a random one: the same frame must fit to the same circle every
	// time it is measured, or a re-run of a job produces a different image.
	state := uint64(0x9E3779B97F4A7C15)
	pick := func() point {
		state = state*6364136223846793005 + 1442695040888963407
		return pts[int((state>>33)%uint64(len(pts)))]
	}
	inliers := func(c Limb) int {
		n := 0
		for _, p := range pts {
			if math.Abs(math.Hypot(p.x-c.CX, p.y-c.CY)-c.R) <= pairInlierPx {
				n++
			}
		}
		return n
	}
	best, bestN := Limb{}, 0
	for i := 0; i < pairConsensusTries; i++ {
		c, ok := kasaCircle([]point{pick(), pick(), pick()})
		if !ok || c.R < 4 {
			continue
		}
		if n := inliers(c); n > bestN {
			best, bestN = c, n
		}
	}
	if bestN < minLimbPoints {
		return Limb{}, false
	}
	kept := make([]point, 0, bestN)
	for _, p := range pts {
		if math.Abs(math.Hypot(p.x-best.CX, p.y-best.CY)-best.R) <= pairInlierPx {
			kept = append(kept, p)
		}
	}
	return fitFrom(kept)
}

// refineAgainst re-locates a circle sub-pixel along radial rays, but ONLY on the azimuths its own
// classified points occupy.
//
// The restriction is what makes the refinement safe on a crescent. The search span is a fixed
// fraction of the radius — twenty-four pixels at these plate scales — while the two bodies sit only
// a crescent-width apart, so a ray fired down an azimuth where this body has no boundary would
// happily lock onto the other one and report it as this circle's limb.
func refineAgainst(im *fits.Image, coarse Limb, pts []point, dir edgeDirection) (Limb, bool) {
	occupied := azimuthCoverage(pts, coarse)
	out := make([]point, 0, limbAzimuths)
	for i := 0; i < limbAzimuths; i++ {
		if !occupied[i] {
			continue
		}
		a := 2 * math.Pi * float64(i) / limbAzimuths
		cos, sin := math.Cos(a), math.Sin(a)
		if r, ok := edgeByInflection(im, coarse, cos, sin, dir, limbSearchSpan); ok {
			out = append(out, point{coarse.CX + r*cos, coarse.CY + r*sin})
		}
	}
	if len(out) < minLimbPoints {
		return Limb{}, false
	}
	fine, ok := fitFrom(out)
	if !ok {
		return Limb{}, false
	}
	return fine, fine.ArcDeg >= pairMinArcDeg
}

// azimuthCoverage marks which of the refinement's azimuth bins a body's points reach, widened by
// one bin either side so a ray is not refused for landing between two measured points.
func azimuthCoverage(pts []point, c Limb) []bool {
	hit := make([]bool, limbAzimuths)
	for _, p := range pts {
		a := math.Atan2(p.y-c.CY, p.x-c.CX)
		if a < 0 {
			a += 2 * math.Pi
		}
		b := int(a / (2 * math.Pi) * limbAzimuths)
		for _, k := range [3]int{b - 1, b, b + 1} {
			hit[(k+limbAzimuths)%limbAzimuths] = true
		}
	}
	return hit
}

// fitMoon fits the occulter and refuses it unless it is physically plausible against the Sun it was
// found beside. A wrong occulter is worse than none — it masks real Sun and exposes real Moon — so
// every gate here fails towards "no eclipse".
func fitMoon(im *fits.Image, pts []point, sun Limb, sky, disc float64) (Limb, bool) {
	if len(pts) < minLimbPoints {
		return Limb{}, false
	}
	moon, ok := fitRobust(pts)
	if !ok || moon.R <= 4 || moon.ArcDeg < pairMinArcDeg {
		return Limb{}, false
	}
	if fine, ok := refineAgainst(im, moon, pts, edgeRising); ok {
		moon = fine
	}
	if ratio := moon.R / sun.R; ratio < pairMoonRadiusLo || ratio > pairMoonRadiusHi {
		return Limb{}, false
	}
	// An occulter that does not reach the Sun is not an occulter. This also rejects the degenerate
	// answer where both fits landed on the same circle.
	if math.Hypot(moon.CX-sun.CX, moon.CY-sun.CY) >= sun.R+moon.R {
		return Limb{}, false
	}
	// AND IT HAS TO BE OPAQUE. Everything above is geometry, and geometry alone cannot tell an
	// occulting body from a dark FILAMENT — which is the failure this gate exists for. A filament
	// running across the disc dips below the mask threshold, so its outline becomes boundary points,
	// and every one of them steps dark-inside-to-bright-outside exactly as the Moon's limb does. Fitted
	// together they produce a plausible circle of plausible radius sitting plausibly over the Sun. The
	// difference is depth: the Moon passes no light at all, while a filament is only about half a
	// magnitude down. So the interior is measured, and anything not close to the sky's own level is
	// refused.
	if lvl := interiorLevel(im, moon); lvl > sky+pairOcculterMaxLevel*(disc-sky) {
		return Limb{}, false
	}
	return moon, true
}

// interiorLevel is the median brightness well inside a circle — eroded to a fraction of the radius so
// the edge's own blur, and any misplacement of the fit, stay out of it.
func interiorLevel(im *fits.Image, c Limb) float64 {
	r := pairInteriorFrac * c.R
	if r < 2 {
		return math.Inf(1) // too small to judge; refuse rather than guess
	}
	r2 := r * r
	vals := make([]float32, 0, 1<<12)
	y0, y1 := clampInt(int(c.CY-r), 0, im.H-1), clampInt(int(c.CY+r)+1, 0, im.H-1)
	x0, x1 := clampInt(int(c.CX-r), 0, im.W-1), clampInt(int(c.CX+r)+1, 0, im.W-1)
	for y := y0; y <= y1; y++ {
		dy := float64(y) - c.CY
		for x := x0; x <= x1; x++ {
			dx := float64(x) - c.CX
			if dx*dx+dy*dy <= r2 {
				vals = append(vals, im.Pix[0][y*im.W+x])
			}
		}
	}
	if len(vals) == 0 {
		return math.Inf(1)
	}
	return float64(imgops.Percentile(imgops.Subsample(vals, 50000), 50))
}

// overlapFraction is the fraction of the solar disc the occulter covers: the analytic area of the
// lens the two circles share, over the Sun's own area.
//
// It is computed from the geometry rather than by counting dark pixels because the pixel count
// moves with the exposure and with the scattered-light aureole, while the circles do not.
func OverlapFraction(sun, moon Limb) float64 {
	if sun.R <= 0 || moon.R <= 0 {
		return 0
	}
	d := math.Hypot(moon.CX-sun.CX, moon.CY-sun.CY)
	switch {
	case d >= sun.R+moon.R:
		return 0
	case d <= math.Abs(sun.R-moon.R):
		// One disc contains the other: the covered area is the smaller disc's, capped at the Sun's.
		return math.Min(1, (moon.R*moon.R)/(sun.R*sun.R))
	}
	r1, r2 := sun.R, moon.R
	a1 := r1 * r1 * math.Acos(clampF((d*d+r1*r1-r2*r2)/(2*d*r1), -1, 1))
	a2 := r2 * r2 * math.Acos(clampF((d*d+r2*r2-r1*r1)/(2*d*r2), -1, 1))
	tri := 0.5 * math.Sqrt(math.Max(0,
		(-d+r1+r2)*(d+r1-r2)*(d-r1+r2)*(d+r1+r2)))
	return clampF((a1+a2-tri)/(math.Pi*r1*r1), 0, 1)
}

// FitGeometry is the single place the choice between the one-body and two-body fit is made, exported
// so a re-finish resolves the same geometry the run did rather than a second, different answer.
func FitGeometry(im *fits.Image, twoBody bool) (Pair, bool) { return fitGeometry(im, twoBody) }

// fitGeometry is the single place the choice between the one-body and two-body fit is made.
//
// The flag is threaded from the preset rather than decided here by looking for an occulter, and
// that is deliberate. FitPair agrees with FitLimb on a full disc to a tenth of a pixel, which is
// close enough to be right and NOT close enough to be identical — and a tenth of a pixel in the
// fitted centre moves every resampled pixel of every frame. Choosing the estimator by what the
// image seems to contain would therefore make an ordinary solar run's output depend on whether the
// occulter search happened to fire, which is not a trade anyone asked for. An eclipse run says so.
func fitGeometry(im *fits.Image, twoBody bool) (Pair, bool) {
	if !twoBody {
		l, ok := FitLimb(im)
		return Pair{Sun: l}, ok
	}
	return FitPair(im)
}
