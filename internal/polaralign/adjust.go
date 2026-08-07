package polaralign

import (
	"errors"
	"math"
	"time"
)

// Following the adjustment live, while the user has both hands on the bolts.
//
// The obvious design is to re-measure the axis from every new frame, but one frame cannot measure an
// axis — it takes an arc. The next idea is to infer the two bolt turns from how the frame centre moved,
// which needs a least-squares fit, a de-rotation of the tracking, and a singularity wherever the
// telescope happens to point due east or west. None of that is necessary.
//
// The target is a FIXED POINT OF SKY, and it stays fixed no matter how far the user has got:
//
//	Let R₀ be the rotation the bolts must still apply at the start, so the target is T₀ = R₀·C₀.
//	After the user has turned the bolts by some U, the axis is A = U·A₀ and the remaining rotation is
//	R = R₀U⁻¹, so the new target is R·(U·C₀) = R₀U⁻¹U·C₀ = R₀·C₀ = T₀.
//
// Tracking does not disturb it either, and not approximately — exactly. Tracking turns the telescope
// about the mount's own axis, R₀·Rot(A₀, δ) = Rot(R₀A₀, δ)·R₀ = Rot(pole, δ)·R₀ identically, so the
// target simply rides the sky: its J2000 coordinates do not change at all while the mount tracks.
//
// So the live loop is: solve a frame, project one fixed sky point into it, draw it. No fitting, no
// singular geometry, no de-rotation, and nothing that can quietly diverge. What it gives up is a live
// split of the remaining error into altitude and azimuth — for that, the user measures again, which is
// the honest answer anyway.

// siderealDegPerSec is how fast the sky turns. Kept local so this package depends only on astro and
// fits; TestLive_SiderealRateMatchesTheHardwareLayer holds it equal to device.SiderealArcsecPerSec.
const siderealDegPerSec = 15.0410686 / 3600

// suspectFactor and suspectFloorDeg together decide when the frame centre has run so far from the
// target that the measurement behind it must be stale.
//
// The factor alone is not enough, in both directions. A session that began already aligned has no
// journey to be a multiple of, so any twitch is infinitely many times it; and turning a bolt the wrong
// way legitimately doubles the distance, which is a mistake to be shown on screen, not a reason to
// throw the measurement away. The floor makes the test mean "further than any hand on a bolt would
// plausibly have moved it".
const (
	suspectFactor   = 2.0
	suspectFloorDeg = 1.0
)

// minScalableGapDeg is the smallest starting gap the remaining-error scaling can be built on. Below it
// the mount was already aligned when the adjustment began, and there is no journey to measure progress
// along.
const minScalableGapDeg = 1.0 / 60

// suspectJumpDeg is how far the frame centre may move between two consecutive frames. Bolts move the
// sky slowly and by hand; a jump this size is a slew, a meridian flip, or a kicked tripod.
const suspectJumpDeg = 2.0

// ErrNoSolution is returned when a frame carries no usable plate solution.
var ErrNoSolution = errors.New("the frame has no usable plate solution")

// Live follows the adjustment. It is created from the measurement and fed one solved frame at a time.
type Live struct {
	site Site
	opt  FitOptions
	// tracking says whether the mount's drive is running. It decides only whether the target rides the
	// sky or stays put over the ground.
	tracking bool

	// target is where the frame centre has to end up, as a direction over the GROUND at startAt.
	target  hVec3
	startAt time.Time

	initialArcmin float64 // the measured polar error, which the marker distance is calibrated against
	initialGapDeg float64 // how far the centre was from the target to begin with

	lastCentre hVec3
	haveLast   bool
}

// LiveState is one update of the adjustment.
type LiveState struct {
	Target Target `json:"target"`
	// RemainingArcmin is how much polar error is left, scaled from how much of the journey to the
	// target the frame centre has made. It is an estimate — the authoritative number comes from
	// measuring again — but it is monotone, never singular, and right at both ends.
	RemainingArcmin float64 `json:"remaining_arcmin"`
	Quality         string  `json:"quality"`
	// Suspect marks a frame the session no longer trusts: the centre has run away from the target, or
	// jumped further than a hand on a bolt can move it. The measurement is stale — start again.
	Suspect bool `json:"suspect,omitempty"`
}

// NewLive starts the adjustment from a measured correction and the frame it was measured on.
func NewLive(c Correction, f Frame, tracking bool, opt FitOptions) (*Live, error) {
	if f.WidthPx <= 0 || f.HeightPx <= 0 || f.At.IsZero() {
		return nil, ErrNoSolution
	}
	centre := frameCentreDir(f, c.site, opt)
	target := c.rotation().apply(centre)
	return &Live{
		site:          c.site,
		opt:           opt,
		tracking:      tracking,
		target:        target,
		startAt:       f.At,
		initialArcmin: c.TotalArcmin,
		initialGapDeg: angleBetween(centre, target),
	}, nil
}

// Update folds in a freshly solved frame.
func (l *Live) Update(f Frame) (LiveState, error) {
	if l == nil {
		return LiveState{}, ErrNoSolution
	}
	if f.WidthPx <= 0 || f.HeightPx <= 0 || f.At.IsZero() {
		return LiveState{}, ErrNoSolution
	}
	centre := frameCentreDir(f, l.site, l.opt)
	target := l.targetAt(f.At)

	gap := angleBetween(centre, target)
	state := LiveState{RemainingArcmin: l.remaining(gap)}
	state.Quality = quality(state.RemainingArcmin)

	if l.haveLast && angleBetween(centre, l.lastCentre) > suspectJumpDeg {
		state.Suspect = true
	}
	if gap > l.initialGapDeg*suspectFactor+suspectFloorDeg {
		state.Suspect = true
	}
	l.lastCentre, l.haveLast = centre, true

	raJ2000, decJ2000 := skyFromDir(target, l.site, f.At, l.opt)
	if t, ok := targetPixel(f, raJ2000, decJ2000, gap); ok {
		state.Target = t
	}
	return state, nil
}

// targetAt is where the target sits over the ground at time t.
//
// With the drive running the telescope is carried round the mount's axis, and the identity above turns
// that into the target being carried round the celestial pole at the same rate — which is exactly the
// sky's own motion, so its J2000 position never moves. With the drive stopped the telescope stays put
// over the ground and so does the target, while the sky slides past it.
func (l *Live) targetAt(t time.Time) hVec3 {
	if !l.tracking {
		return l.target
	}
	elapsed := t.Sub(l.startAt).Seconds()
	// Negative, because advancing hour angle is a negative right-hand turn about the north pole —
	// see TestRotateAboutH_SiderealSignAtThePole.
	return rotateAboutH(l.target, ncpHorizon(l.site.LatDeg), -siderealDegPerSec*elapsed)
}

// remaining scales the measured polar error by how far the frame centre still has to travel.
//
// The map from "polar error left" to "how far the centre is from the target" is linear, so their ratio
// is the fraction of the correction still outstanding. It is exact at the start by construction and
// exact at the end because both go to zero together; in between it is a good enough guide to whether
// the last quarter turn helped.
//
// Unless the session began already aligned, in which case both ends of that ratio are zero and it says
// nothing at all — dividing by the sliver of a gap that rounding left behind turns a nudge of the
// tripod into a reading of hundreds of degrees. There is nothing to scale then, so the distance the
// field has moved is reported directly: it is what the marker on screen is showing anyway, and it is
// the right answer to "how far have I just knocked this out".
func (l *Live) remaining(gapDeg float64) float64 {
	if l.initialGapDeg < minScalableGapDeg {
		return gapDeg * 60
	}
	return l.initialArcmin * gapDeg / l.initialGapDeg
}

// frameCentreDir is the direction the telescope was mechanically pointing when the frame was taken.
func frameCentreDir(f Frame, site Site, opt FitOptions) hVec3 {
	cx, cy := f.centrePix()
	ra, dec := f.WCS.PixToSky(cx, cy)
	return sampleDir(Sample{RADeg: ra, DecDeg: dec, At: f.At}, site, opt)
}

// targetPixel projects a sky position into a frame and fills in the distances.
func targetPixel(f Frame, raDeg, decDeg, gapDeg float64) (Target, bool) {
	x, y, ok := f.WCS.SkyToPix(raDeg, decDeg)
	if !ok {
		return Target{}, false
	}
	cx, cy := f.centrePix()
	return Target{
		RADeg: raDeg, DecDeg: decDeg,
		X: x, Y: y,
		NX:           x / float64(f.WidthPx),
		NY:           y / float64(f.HeightPx),
		OffsetPx:     math.Hypot(x-cx, y-cy),
		OffFrame:     x < 0 || y < 0 || x >= float64(f.WidthPx) || y >= float64(f.HeightPx),
		OffsetArcmin: gapDeg * 60,
	}, true
}

// ncpHorizon is the NORTH celestial pole, in both hemispheres. The sky turns about it in one sense
// everywhere on Earth; poleHorizon, which flips south of the equator, is the direction the MOUNT has
// to be aimed at, and the two must not be confused.
func ncpHorizon(latDeg float64) hVec3 { return haVec3{Z: 1}.horizon(latDeg) }
