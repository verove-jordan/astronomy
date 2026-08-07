package guide

import (
	"math"
	"time"
)

// SiderealArcsecPerSec is the rate the sky turns. Duplicated from internal/device rather than imported
// so this package stays free of the hardware layer — the value is a property of the Earth, and the two
// copies are checked against each other by a test.
const SiderealArcsecPerSec = 15.0410686

// DefaultGuideRateFraction is the pulse speed to use when the mount will not say what it is configured
// for. Half sidereal is what most mounts ship with, and it is slow enough that a correction settles
// quickly rather than ringing.
const DefaultGuideRateFraction = 0.5

// PulseFor converts a correction in axis arcseconds into the rate and duration of a guide pulse.
//
// rateArcsecPerSec is the speed the pulse is delivered at, normally the mount's own configured
// autoguide rate times sidereal. The returned rate carries the correction's sign, and ok is false when
// there is nothing worth commanding — a caller must not turn that into a zero-length pulse, because
// some drivers would still send the start frame.
func PulseFor(arcsec, rateArcsecPerSec float64) (rate float64, d time.Duration, ok bool) {
	if arcsec == 0 || math.IsNaN(arcsec) || math.IsInf(arcsec, 0) {
		return 0, 0, false
	}
	if rateArcsecPerSec <= 0 || math.IsNaN(rateArcsecPerSec) || math.IsInf(rateArcsecPerSec, 0) {
		return 0, 0, false
	}
	d = time.Duration(math.Abs(arcsec) / rateArcsecPerSec * float64(time.Second))
	if d <= 0 {
		return 0, 0, false
	}
	return math.Copysign(rateArcsecPerSec, arcsec), d, true
}

// GuideRateArcsecPerSec turns a mount's configured autoguide rate, as a fraction of sidereal, into the
// pulse speed to use. An unreported or nonsensical fraction falls back to the default rather than
// producing a rate of zero, which would silently stop the guider correcting anything.
func GuideRateArcsecPerSec(fraction float64) float64 {
	if fraction <= 0 || fraction > 1 || math.IsNaN(fraction) {
		fraction = DefaultGuideRateFraction
	}
	return fraction * SiderealArcsecPerSec
}
