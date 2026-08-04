package polaralign

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// Measuring where the mount's polar axis REALLY points, from plate-solved frames.
//
// Turn the telescope about its right-ascension axis and the optical axis sweeps a circle on the sky,
// centred on that axis. Solve three frames along the sweep, express them in a ground-fixed frame, and
// the circle's centre IS the mount's polar axis — no pointing model, no encoders, no alignment, no
// mount driver at all. That last part is why the rotation between frames can be done by hand: the
// method never needs to know how far the axis turned, only that it did.
//
// This is the N.I.N.A. "three point polar alignment" method rather than SharpCap's, deliberately: it
// works from any part of the sky, so a tree, a roof or a neighbour's wall in front of Polaris costs
// nothing.

// How far the mount has to be turned, and why 60° rather than the 20–30° that feels like enough.
//
// The axis uncertainty falls as the SQUARE of the arc, because what pins the circle's centre is the
// curvature of the arc, not its length. That was measured on this implementation, not assumed —
// TestFitAxis_ErrorScalesWithTheSquareOfTheArc holds the scaling, and the table it is derived from
// (400 trials per cell, 1″ of plate-solve noise, three frames) reads:
//
//	arc      20°     25°     30°     45°     60°     90°
//	rms     1.27′   0.82′   0.57′   0.26′   0.15′   0.07′
//	p95     2.59′   1.66′   1.15′   0.52′   0.29′   0.14′
//
// So 30° — the obvious choice, and what a first draft of this feature asked for — leaves a one-in-
// twenty chance of being over an arcminute out, which is the whole thing we are trying to fix. 60°
// gets p95 to 0.29′ and stays under an arcminute even at 2″ of solve noise.
//
// Adding frames, by contrast, buys almost nothing: six frames over the same arc improve the result by
// about 12%, because the two ENDS carry nearly all the curvature information. Spend the user's time on
// turning the axis further, never on taking more pictures.
const (
	// minArcDeg is the smallest rotation the fit will accept at all.
	minArcDeg = 5
	// goodArcDeg is where the geometry stops being the limiting factor. Below it the answer is
	// reported as weakly constrained rather than refused — a rough number beats no number when the
	// sky is closing in.
	goodArcDeg = 45
	// wantArcDeg is what the UI asks for.
	wantArcDeg = 60
)

const (
	// minRadiusDeg guards the other degeneracy: a telescope pointed AT the pole traces a circle of no
	// size, and a circle of no size has no well-defined centre.
	minRadiusDeg = 15
	// goodRadiusDeg is where the cone is comfortably wide. Between the two the fit works but is
	// noticeably noisier, so the user is told to point further from the pole.
	goodRadiusDeg = 25
)

// assumedSolveArcsec is the per-axis uncertainty of one plate-solved frame CENTRE. It is much better
// than the 1–2″ of a single star because the centre is an average over hundreds of them; what stops it
// going lower is systematics — proper motions, optical distortion, differential refraction. Used to
// turn the fit geometry into an honest error bar when there are too few frames to measure the noise.
const assumedSolveArcsec = 1.0

// maxResidualArcsec is how far a frame may sit from the fitted circle before the set is rejected. A
// mount that was only turned in right ascension keeps every frame on one circle by construction, so a
// large residual means something else moved: the declination axis, a meridian flip, or the tripod.
const maxResidualArcsec = 60

var (
	// ErrTooFewSamples is returned for fewer than three points — two points lie on infinitely many
	// circles.
	ErrTooFewSamples = errors.New("polar alignment needs at least three solved frames")
	// ErrArcTooSmall means the telescope barely moved between frames.
	ErrArcTooSmall = errors.New("the mount was not rotated far enough between frames")
	// ErrDegenerate means the geometry cannot determine an axis: coincident frames, or a field so
	// close to the pole that the circle has no radius.
	ErrDegenerate = errors.New("the frames do not describe a usable circle")
	// ErrInconsistent means the frames do not lie on ONE circle, so something other than the right
	// ascension axis moved between them.
	ErrInconsistent = errors.New("the frames do not lie on one circle — the declination axis moved, the mount flipped, or the tripod was knocked")
)

// Warning codes. The backend emits codes and the frontend translates them, as internal/align does.
const (
	WarnWeakArc     = "weak_arc"     // the rotation was short; the answer is rough
	WarnNearPole    = "near_pole"    // the field is close to the axis, so the circle is small
	WarnNoResidual  = "no_residual"  // three frames always fit a circle exactly; nothing was checked
	WarnLowAltitude = "low_altitude" // a frame was low enough for the refraction model to be shaky
)

// minSampleAltDeg is where refraction stops being reliable: it is over five arcminutes at 10°, and its
// dependence on pressure and temperature — neither of which we know — is a tenth of that.
const minSampleAltDeg = 10

// Site is where the telescope stands. East-positive longitude, as everywhere else in this repo.
type Site struct {
	LatDeg float64
	LonDeg float64
}

// FitOptions tune the measurement. The zero value applies refraction, which is the physically correct
// choice: a plate solve reports where a star IS, while the telescope is pointed at where it APPEARS.
type FitOptions struct {
	// NoRefraction turns the correction off, for comparison against tools that leave it out.
	NoRefraction bool
}

func (o FitOptions) refract() bool { return !o.NoRefraction }

// Sample is one plate-solved frame centre.
type Sample struct {
	// RADeg/DecDeg are J2000, which is what every plate solve in this app produces.
	RADeg  float64
	DecDeg float64
	// At is the MID-exposure instant. Hour angle is computed from it, so the start of a 30-second
	// exposure is fifteen seconds — four arcminutes of sky rotation — wrong.
	At time.Time
}

// Axis is where the polar axis points, and how much the measurement is worth.
type Axis struct {
	AltDeg float64 `json:"alt_deg"` // where the mount's RA axis really points
	AzDeg  float64 `json:"az_deg"`
	// RadiusDeg is the angle between the polar axis and the optical axis — the radius of the circle
	// the frames traced.
	RadiusDeg float64 `json:"radius_deg"`
	// ArcDeg is how far the mount was rotated across all the samples.
	ArcDeg float64 `json:"arc_deg"`
	// ResidualArcsec is the RMS distance of the samples from the fitted circle: the measure of whether
	// anything moved that should not have. It is identically zero for three samples — three points
	// always lie on exactly one circle — so it only says something from four frames on, and
	// WarnNoResidual is raised when it does not.
	ResidualArcsec float64 `json:"residual_arcsec"`
	// SigmaArcsec is the one-sigma uncertainty of the axis in its WORST-constrained direction, from
	// the geometry of the arc itself. This is the number that says whether to believe the answer; the
	// error ellipse is very elongated, so a single "total error" figure flatters it.
	SigmaArcsec float64 `json:"sigma_arcsec"`
	Samples     int     `json:"samples"`
	// Warnings are codes the UI translates: the fit succeeded but something about it is worth saying.
	Warnings []string `json:"warnings,omitempty"`
}

// vec rebuilds the axis direction in the horizon frame.
func (a Axis) vec() hVec3 { return horizonVec(a.AltDeg, a.AzDeg) }

// FitAxis recovers the mount's polar axis from frames solved along one rotation of the RA axis.
func FitAxis(samples []Sample, site Site, opt FitOptions) (Axis, error) {
	if len(samples) < 3 {
		return Axis{}, ErrTooFewSamples
	}
	dirs := make([]hVec3, len(samples))
	var warnings []string
	for i, s := range samples {
		if !finite(s.RADeg) || !finite(s.DecDeg) || s.At.IsZero() {
			return Axis{}, fmt.Errorf("sample %d is incomplete", i+1)
		}
		dirs[i] = sampleDir(s, site, opt)
		if alt, _ := dirs[i].altAz(); alt < minSampleAltDeg {
			warnings = appendOnce(warnings, WarnLowAltitude)
		}
	}

	axis, lambda, ok := fitCircleAxis(dirs, poleHorizon(site.LatDeg))
	if !ok {
		return Axis{}, ErrDegenerate
	}

	radius, residual := circleRadius(dirs, axis)
	if radius < minRadiusDeg || radius > 180-minRadiusDeg {
		return Axis{}, fmt.Errorf("%w: the field is only %.1f° from the mount's axis", ErrDegenerate, radius)
	}
	arc := spanAbout(dirs, axis)
	if arc < minArcDeg {
		return Axis{}, fmt.Errorf("%w: only %.1f° of rotation", ErrArcTooSmall, arc)
	}
	if len(samples) > 3 && residual > maxResidualArcsec {
		return Axis{}, fmt.Errorf("%w: the frames miss one circle by %.0f″", ErrInconsistent, residual)
	}

	if arc < goodArcDeg {
		warnings = appendOnce(warnings, WarnWeakArc)
	}
	if radius < goodRadiusDeg {
		warnings = appendOnce(warnings, WarnNearPole)
	}
	if len(samples) < 4 {
		warnings = appendOnce(warnings, WarnNoResidual)
	}

	alt, az := axis.altAz()
	return Axis{
		AltDeg: alt, AzDeg: az,
		RadiusDeg:      radius,
		ArcDeg:         arc,
		ResidualArcsec: residual,
		SigmaArcsec:    axisSigmaArcsec(lambda, residual, len(samples)),
		Samples:        len(samples),
		Warnings:       warnings,
	}, nil
}

// axisSigmaArcsec turns the fit's own geometry into a one-sigma error bar for the axis.
//
// Tilting the axis by ε about a direction in the circle's plane moves each sample by ε times its
// distance along that direction, so the information the frames carry about a tilt is exactly the
// scatter matrix's eigenvalue in that direction. The SMALLER of the two in-plane eigenvalues is
// therefore the weakest direction — the one along which a short arc tells you almost nothing — and
// dividing the per-frame noise by its square root gives the uncertainty there.
//
// That single expression subsumes every heuristic about how long an arc "should" be: a short arc, a
// small cone and a noisy solve all show up in it, correctly weighted, with no thresholds invented.
func axisSigmaArcsec(lambda [3]float64, residualArcsec float64, n int) float64 {
	// lambda comes back with the plane normal's (smallest) eigenvalue first; the next one up is the
	// weak in-plane direction.
	weak := lambda[1]
	if weak <= 0 {
		return math.Inf(1)
	}
	sigma := assumedSolveArcsec
	// From four frames on, the scatter about the circle measures the noise instead of assuming it. Use
	// whichever is worse: an unusually clean set does not license claiming more precision than the
	// solver can deliver.
	if n > 3 {
		if measured := residualArcsec * math.Sqrt(float64(n)/float64(n-3)); measured > sigma {
			sigma = measured
		}
	}
	return sigma / math.Sqrt(weak)
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func appendOnce(list []string, code string) []string {
	for _, c := range list {
		if c == code {
			return list
		}
	}
	return append(list, code)
}

// sampleDir turns one solved frame centre into the direction the TELESCOPE was mechanically pointed,
// in the ground-fixed horizon frame.
//
// Two conversions here are not optional. Precession, because a plate solve speaks J2000 while local
// sidereal time speaks the equinox of date — 0.36° apart in 2026, twenty times the error being
// measured. And refraction, because the solve reports where the star is while the tube is aimed at
// where it appears; skipping it biases the fit by an arcminute or so at moderate altitudes.
func sampleDir(s Sample, site Site, opt FitOptions) hVec3 {
	ra, dec := astro.PrecessFromJ2000(s.RADeg, s.DecDeg, s.At)
	ha := astro.LST(s.At, site.LonDeg) - ra
	v := haVec(ha, dec).horizon(site.LatDeg)
	if !opt.refract() {
		return v
	}
	alt, az := v.altAz()
	return horizonVec(astro.ApparentAltitude(alt), az)
}

// skyFromDir is sampleDir backwards: a mechanical pointing direction to the J2000 coordinates a plate
// solve would report for it. The target marker needs this to find its pixel.
func skyFromDir(v hVec3, site Site, at time.Time, opt FitOptions) (raDeg, decDeg float64) {
	if opt.refract() {
		alt, az := v.altAz()
		v = horizonVec(trueAltitude(alt), az)
	}
	ha, dec := v.hourAngle(site.LatDeg).haDec()
	ra := astro.LST(at, site.LonDeg) - ha
	return astro.PrecessToJ2000(norm360(ra), dec, at)
}

// trueAltitude inverts astro.ApparentAltitude. Refraction is monotonic and shallow, so simple fixed
// point iteration converges to well under a milliarcsecond in a handful of passes.
func trueAltitude(apparentAltDeg float64) float64 {
	t := apparentAltDeg
	for i := 0; i < 6; i++ {
		next := apparentAltDeg - astro.Refraction(t)/60
		if math.Abs(next-t) < 1e-9 {
			return next
		}
		t = next
	}
	return t
}

// fitCircleAxis finds the axis of the small circle the directions lie on, and returns the scatter
// matrix's eigenvalues ASCENDING — the first is the out-of-plane one (the residual), the second is the
// weak in-plane direction that sets how well the axis is pinned down.
//
// Every point satisfies A·p = cos(radius) for the unknown unit axis A, so the points lie on a PLANE
// with normal A. Subtracting their centroid and taking the eigenvector of the smallest eigenvalue of
// the scatter matrix is the total-least-squares plane fit — total least squares rather than ordinary,
// because the error is in the points, not in one distinguished coordinate.
//
// The sign is then fixed by `toward`: a plane normal is defined only up to sign, and the mount's axis
// is within a few degrees of the celestial pole, so "the one on the pole's side" is unambiguous in a
// way that reasoning about the direction of rotation would not be.
func fitCircleAxis(dirs []hVec3, toward hVec3) (hVec3, [3]float64, bool) {
	var mean hVec3
	for _, d := range dirs {
		mean.N += d.N
		mean.E += d.E
		mean.U += d.U
	}
	n := float64(len(dirs))
	mean = hVec3{N: mean.N / n, E: mean.E / n, U: mean.U / n}

	var s [3][3]float64
	for _, d := range dirs {
		v := [3]float64{d.E - mean.E, d.N - mean.N, d.U - mean.U} // (E, N, U), as everywhere else
		for i := 0; i < 3; i++ {
			for j := 0; j < 3; j++ {
				s[i][j] += v[i] * v[j]
			}
		}
	}

	vals, vecs := jacobiEigen3(s)
	order := [3]int{0, 1, 2}
	sort.Slice(order[:], func(a, b int) bool { return vals[order[a]] < vals[order[b]] })
	var sorted [3]float64
	for i, k := range order {
		sorted[i] = vals[k]
	}

	// The SECOND smallest eigenvalue is what says the points spread out WITHIN their plane. Three
	// coincident frames give a perfect, zero-residual plane fit through a single point, and only this
	// catches it — the smallest eigenvalue is happily zero in both the best case and the worst.
	if sorted[1] < 1e-14 {
		return hVec3{}, sorted, false
	}

	// The scatter matrix is built in the (E, N, U) Cartesian order, so its eigenvectors come back in
	// that order too.
	lo := order[0]
	axis, ok := hVec3{E: vecs[0][lo], N: vecs[1][lo], U: vecs[2][lo]}.unit()
	if !ok {
		return hVec3{}, sorted, false
	}
	if axis.dot(toward) < 0 {
		axis = hVec3{N: -axis.N, E: -axis.E, U: -axis.U}
	}
	return axis, sorted, true
}

// circleRadius reports the mean angular radius of the fitted circle and the RMS scatter of the samples
// about it, in arcseconds.
func circleRadius(dirs []hVec3, axis hVec3) (radiusDeg, residualArcsec float64) {
	radii := make([]float64, len(dirs))
	for i, d := range dirs {
		radii[i] = angleBetween(axis, d)
		radiusDeg += radii[i]
	}
	radiusDeg /= float64(len(dirs))
	for _, r := range radii {
		residualArcsec += (r - radiusDeg) * (r - radiusDeg)
	}
	return radiusDeg, math.Sqrt(residualArcsec/float64(len(dirs))) * 3600
}

// spanAbout is how far the mount turned: the angular span of the samples measured AROUND the axis,
// which is the quantity the fit's accuracy actually depends on.
func spanAbout(dirs []hVec3, axis hVec3) float64 {
	ref, ok := perpendicularTo(dirs[0], axis)
	if !ok {
		return 0
	}
	side := axis.cross(ref)
	lo, hi := 0.0, 0.0
	for _, d := range dirs[1:] {
		p, ok := perpendicularTo(d, axis)
		if !ok {
			continue
		}
		a := norm180(math.Atan2(p.dot(side), p.dot(ref)) * rad2deg)
		lo = math.Min(lo, a)
		hi = math.Max(hi, a)
	}
	return hi - lo
}

// perpendicularTo projects v onto the plane through the origin normal to axis, and normalizes.
func perpendicularTo(v, axis hVec3) (hVec3, bool) {
	k := v.dot(axis)
	return hVec3{N: v.N - k*axis.N, E: v.E - k*axis.E, U: v.U - k*axis.U}.unit()
}

// jacobiEigen3 diagonalizes a symmetric 3×3 matrix, returning eigenvalues and the matrix whose
// COLUMNS are the matching eigenvectors.
//
// Hand-rolled because the repo has no linear-algebra dependency and adding one for a 3×3 would be a
// poor trade. Cyclic Jacobi is a dozen lines, unconditionally stable on symmetric input, and needs no
// starting guess.
func jacobiEigen3(m [3][3]float64) ([3]float64, [3][3]float64) {
	a := m
	v := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

	for sweep := 0; sweep < 32; sweep++ {
		off := math.Abs(a[0][1]) + math.Abs(a[0][2]) + math.Abs(a[1][2])
		if off < 1e-18 {
			break
		}
		for _, pq := range [3][2]int{{0, 1}, {0, 2}, {1, 2}} {
			p, q := pq[0], pq[1]
			if math.Abs(a[p][q]) < 1e-20 {
				continue
			}
			// The rotation angle that zeroes a[p][q]. The tan-half-angle form avoids the cancellation
			// that a naive atan2 of nearly equal diagonal entries suffers.
			theta := (a[q][q] - a[p][p]) / (2 * a[p][q])
			t := 1 / (math.Abs(theta) + math.Sqrt(theta*theta+1))
			if theta < 0 {
				t = -t
			}
			c := 1 / math.Sqrt(t*t+1)
			s := t * c
			for k := 0; k < 3; k++ {
				akp, akq := a[k][p], a[k][q]
				a[k][p] = c*akp - s*akq
				a[k][q] = s*akp + c*akq
			}
			for k := 0; k < 3; k++ {
				apk, aqk := a[p][k], a[q][k]
				a[p][k] = c*apk - s*aqk
				a[q][k] = s*apk + c*aqk
			}
			for k := 0; k < 3; k++ {
				vkp, vkq := v[k][p], v[k][q]
				v[k][p] = c*vkp - s*vkq
				v[k][q] = s*vkp + c*vkq
			}
		}
	}
	return [3]float64{a[0][0], a[1][1], a[2][2]}, v
}
