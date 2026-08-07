package polaralign

import (
	"math"
	"time"
)

// Turning a measured polar axis into two instructions and one rotation.
//
// An equatorial mount has exactly two adjusters: one tilts the polar axis up and down, the other
// swings it left and right. So the answer the user needs is two numbers and two directions — and,
// because numbers in the dark are hard, the rotation those two adjusters apply to the whole telescope,
// which is what target.go turns into a marker to drive into the crosshairs.

// Quality bands, in arcminutes of total axis error. An arcminute is where polar alignment stops being
// the thing limiting a sub, so that is where "excellent" starts.
const (
	excellentArcmin = 1
	goodArcmin      = 3
	fairArcmin      = 10
)

// Quality bands as codes for the UI to translate.
const (
	QualityExcellent = "excellent"
	QualityGood      = "good"
	QualityFair      = "fair"
	QualityPoor      = "poor"
)

// Directions to move the polar axis. "ok" means that axis is already within the excellent band and
// touching it would only make things worse.
const (
	MoveRaise = "raise"
	MoveLower = "lower"
	MoveEast  = "east"
	MoveWest  = "west"
	MoveNone  = "ok"
)

// Correction is what to do about the measured axis.
type Correction struct {
	// AltErrorDeg is how much too high the polar axis points. Negative means too low.
	AltErrorDeg float64 `json:"alt_error_deg"`
	// AzErrorDeg is how far the axis lies to the EAST of the pole's meridian, as a true angle on the
	// sky. Negative is west. This is the number that governs how badly the mount tracks.
	AzErrorDeg float64 `json:"az_error_deg"`
	// AzKnobDeg is the same error expressed as the AZIMUTH the adjuster has to turn through, which is
	// larger than AzErrorDeg by 1/cos(altitude) — a factor of 1.4 at latitude 45°, 2 at 60°.
	//
	// Both are reported because confusing them is a real and specific failure: an observer who turns
	// the azimuth knob by the sky error undershoots by cos(latitude) every single time, converges
	// slowly, and concludes the tool is wrong. Same sign convention as AzErrorDeg — east is positive
	// in both hemispheres.
	AzKnobDeg float64 `json:"az_knob_deg"`
	// TotalArcmin is the great-circle angle between the mount's axis and the pole.
	TotalArcmin float64 `json:"total_arcmin"`
	// AltMove and AzMove say which way to turn each adjuster, so the UI never has to reason about the
	// sign of anything — the southern hemisphere reverses which compass direction "left" means.
	AltMove string `json:"alt_move"`
	AzMove  string `json:"az_move"`
	Quality string `json:"quality"`

	// site and axis are kept so Target can rebuild the rotation without being handed them again.
	site Site
	axis hVec3
}

// Correct works out what the two adjusters have to do.
func Correct(a Axis, site Site) Correction {
	axis := a.vec()
	pole := poleHorizon(site.LatDeg)

	altErr := a.AltDeg - math.Abs(site.LatDeg)

	// How far the axis lies off the pole's meridian, measured against due east. The pole sits at azimuth
	// 0 or 180, so its own east component is zero either way and this one expression means "east" in
	// both hemispheres with no branch.
	azErr := math.Asin(clamp1(axis.dot(hVec3{E: 1}))) * rad2deg

	// The knob turns through azimuth, and azimuth is compressed by cos(altitude) near the pole.
	azKnob := azErr
	if c := math.Cos(a.AltDeg * deg2rad); c > 1e-6 {
		azKnob = math.Asin(clamp1(math.Sin(azErr*deg2rad)/c)) * rad2deg
	}

	total := angleBetween(axis, pole) * 60
	return Correction{
		AltErrorDeg: altErr,
		AzErrorDeg:  azErr,
		AzKnobDeg:   azKnob,
		TotalArcmin: total,
		AltMove:     moveWord(altErr, MoveLower, MoveRaise),
		AzMove:      moveWord(azErr, MoveWest, MoveEast),
		Quality:     quality(total),
		site:        site,
		axis:        axis,
	}
}

// azSign converts an east-positive offset into a change in the azimuth COORDINATE. Azimuth runs
// clockwise from north, so moving east means increasing it near the north pole and decreasing it near
// the south pole. Every user-facing number in this package is east-positive; this is the one place the
// hemisphere enters.
func azSign(latDeg float64) float64 {
	if latDeg < 0 {
		return -1
	}
	return 1
}

// moveWord names the direction to move the axis: positive error means move the OTHER way.
func moveWord(errDeg float64, whenPositive, whenNegative string) string {
	if math.Abs(errDeg)*60 < excellentArcmin {
		return MoveNone
	}
	if errDeg > 0 {
		return whenPositive
	}
	return whenNegative
}

func quality(totalArcmin float64) string {
	switch {
	case totalArcmin < excellentArcmin:
		return QualityExcellent
	case totalArcmin < goodArcmin:
		return QualityGood
	case totalArcmin < fairArcmin:
		return QualityFair
	default:
		return QualityPoor
	}
}

// rotation is what the two adjusters do to the WHOLE telescope when they are turned as instructed.
//
// Azimuth first, then altitude: the azimuth stage swings the axis onto the pole's meridian without
// touching its altitude, and the altitude stage then lifts or drops it along that meridian. In that
// order the axis lands on the pole exactly, at any error size — there is no small-angle assumption
// here, which matters because the first measurement of a badly set-up mount can be several degrees.
//
// It is deliberately NOT the shortest rotation from the axis to the pole. Both put the axis on the
// pole, but they differ by a twist about the pole, and that twist moves the rest of the sky by degrees.
// Only this one is what the hardware actually does.
func (c Correction) rotation() rot {
	// AzKnobDeg is east-positive; the azimuth COORDINATE has to move the other way in the south.
	azDeg := -c.AzKnobDeg * azSign(c.site.LatDeg)
	swung := rotateZenith(c.axis, azDeg)
	pole := poleHorizon(c.site.LatDeg)
	// Both vectors now lie in the meridian plane, so what is left is a plane rotation and atan2 gives
	// it directly — no hemisphere branch, no quadrant to get wrong.
	tilt := math.Atan2(swung.N*pole.U-swung.U*pole.N, swung.N*pole.N+swung.U*pole.U) * rad2deg
	return rot{azDeg: azDeg, tiltDeg: tilt}
}

// rot is the two-adjuster rotation, kept as its two angles rather than a matrix: that is the form the
// hardware has, and the form that cannot represent anything the hardware cannot do.
type rot struct {
	azDeg   float64 // about the vertical, in the direction of increasing azimuth
	tiltDeg float64 // about the east-west horizontal, raising what lies to the north
	// tiltFirst reverses the order of the two stages, which is what inverting one requires. The order
	// only matters at second order in the angles, but an inverse that is only approximately an inverse
	// is a debt that comes due on the largest errors, which is exactly when it is least welcome.
	tiltFirst bool
}

// apply carries a direction through the adjustment.
func (r rot) apply(v hVec3) hVec3 {
	if r.tiltFirst {
		return rotateZenith(rotateEast(v, r.tiltDeg), r.azDeg)
	}
	return rotateEast(rotateZenith(v, r.azDeg), r.tiltDeg)
}

// scaled is a fraction of the adjustment, for stepping toward alignment a bit at a time.
func (r rot) scaled(f float64) rot {
	return rot{azDeg: r.azDeg * f, tiltDeg: r.tiltDeg * f}
}

// inverse undoes the adjustment. The stages run in the opposite order as well as the opposite
// direction, which is the whole content of inverting a composition.
func (r rot) inverse() rot { return rot{azDeg: -r.azDeg, tiltDeg: -r.tiltDeg, tiltFirst: true} }

// poleAzimuth is the compass bearing of the pole the mount must be aimed at.
func poleAzimuth(latDeg float64) float64 {
	if latDeg < 0 {
		return 180
	}
	return 0
}

// misalignedAxis builds the axis of a mount that is out by a known amount: altErrDeg too high, and
// azKnobDeg of AZIMUTH — the angle the adjuster turns through, east positive — away from the pole's
// meridian.
func misalignedAxis(site Site, altErrDeg, azKnobDeg float64) Axis {
	v := horizonVec(math.Abs(site.LatDeg)+altErrDeg,
		poleAzimuth(site.LatDeg)+azKnobDeg*azSign(site.LatDeg))
	alt, az := v.altAz()
	return Axis{AltDeg: alt, AzDeg: az}
}

// MisalignPointing answers the simulator's question: a telescope whose mount is out by a known amount
// is pointed somewhere its owner believes is (raDeg, decDeg) — where is it really pointed?
//
// It exists so the whole camera-alignment feature can be exercised indoors. The simulated observatory
// applies this to everything it reports and everything it draws, so the frames, the mount readout and
// the measurement all describe one consistently misaligned telescope, and a session run against it
// recovers the error that was dialled in.
//
// altErrDeg is how much too HIGH the polar axis sits; azKnobDeg is how far EAST of the pole's meridian,
// as the azimuth the adjuster turns through — the same quantity Correction reports as AzKnobDeg, so
// what is dialled in here is what comes back out. Coordinates are J2000 in and out.
func MisalignPointing(raDeg, decDeg float64, site Site, at time.Time, altErrDeg, azKnobDeg float64) (float64, float64) {
	// Exactly zero has to be exactly a no-op: a simulator with no error configured must behave as it
	// always did, down to the last bit.
	if altErrDeg == 0 && azKnobDeg == 0 {
		return raDeg, decDeg
	}
	opt := FitOptions{}
	// Correct gives the rotation that would put a misaligned axis back on the pole. The simulator wants
	// the opposite: take a mount that is aimed correctly and knock it out by exactly that much.
	knock := Correct(misalignedAxis(site, altErrDeg, azKnobDeg), site).rotation().inverse()

	here := sampleDir(Sample{RADeg: raDeg, DecDeg: decDeg, At: at}, site, opt)
	return skyFromDir(knock.apply(here), site, at, opt)
}
