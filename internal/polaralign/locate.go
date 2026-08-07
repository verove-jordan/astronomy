package polaralign

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// Finding the pole in a single frame — the digital polar scope.
//
// One plate solve is enough to say exactly where the celestial pole falls on the sensor, because the
// pole is not a star but a coordinate: precess it to today and project it through the solution. No
// catalogue, no assumption about the mount, and a plate scale measured from the stars themselves
// rather than guessed from a focal length that moves with the focuser and the temperature.
//
// What one frame CANNOT say is where the mount's polar axis points. The middle of the image is the
// OPTICAL axis; the polar axis is wherever the declination axis and the cone error have put it. A
// single frame contains only starlight, and where the polar axis points is a fact about metal — the
// only way to see it is to move the mount and watch what the field does, which is what FitAxis is for.
//
// So this file does two separate jobs, and the difference between them matters:
//
//	Locate    tells you where the pole IS. Exact, assumes nothing, and solves the genuinely annoying
//	          part of a night: getting the pole into the field at all.
//	RoughAxis turns that into an alignment by ASSERTING that the optical axis is the polar axis, which
//	          is true when the declination axis sits at its 90° index. Then "put the pole on the
//	          crosshairs" means something — exactly as it does in a polar scope, and to about the same
//	          accuracy, which is to say the cone error.

// WarnAssumedOnAxis marks a correction derived from one frame rather than measured from a rotation.
// It is not a caveat to bury: the answer is only as good as the assumption that the telescope is
// looking straight down the right-ascension axis.
const WarnAssumedOnAxis = "assumed_on_axis"

// roughSigmaArcsec is what a one-frame answer is worth: the angle between the optical axis and the
// polar axis, which this mode assumes to be zero and which on real gear is a few tenths of a degree of
// cone error plus however precisely the declination index was set. Half a degree is the honest order
// of magnitude — polar-scope class. The four-frame measurement replaces the assumption with a
// measurement and lands two orders of magnitude better.
const roughSigmaArcsec = 0.5 * 3600

// PoleView is where the pole and its guide star fall on one solved frame.
type PoleView struct {
	// Pole is the celestial pole itself: where the telescope has to be aimed.
	Pole Target `json:"pole"`
	// Star is Polaris, or σ Octantis below the equator — what the eye is looking for, and the thing
	// that makes an image of the pole region readable at a glance.
	Star     Target `json:"star"`
	StarName string `json:"star_name"`
	// StarVisible is false when the guide star is not on the sensor, which is common and fine: the
	// pole marker does not need it.
	StarVisible bool `json:"star_visible"`
}

// Locate reports where the pole and its guide star sit on a solved frame. ok is false when the frame
// carries no usable solution.
func Locate(f Frame, site Site, opt FitOptions) (PoleView, bool) {
	axis, ok := RoughAxis(f, site, opt)
	if !ok {
		return PoleView{}, false
	}
	// The pole marker is exactly the target of a correction that would put the optical axis on the
	// pole — the same computation, so the finder and the alignment can never disagree about where the
	// pole is.
	pole, ok := Correct(axis, site).Target(f, opt)
	if !ok {
		return PoleView{}, false
	}

	view := PoleView{Pole: pole}
	starRA, starDec, name := astro.PoleStar(site.LatDeg >= 0, f.At)
	view.StarName = name
	// PoleStar precesses to the epoch of the request; the solution speaks J2000, so it goes back.
	j2000RA, j2000Dec := astro.PrecessToJ2000(starRA, starDec, f.At)
	if star, ok := targetPixel(f, j2000RA, j2000Dec,
		astro.AngularSeparation(j2000RA, j2000Dec, pole.RADeg, pole.DecDeg)); ok {
		view.Star = star
		view.StarVisible = !star.OffFrame
	}
	return view, true
}

// RoughAxis treats the middle of the frame as the mount's polar axis.
//
// That is true exactly when the telescope is looking down the right-ascension axis — declination at
// its 90° index — and false by the cone error otherwise, which is why the result carries
// WarnAssumedOnAxis and a candid SigmaArcsec. It is the same assumption a polar scope makes, and it
// buys the same thing: a usable alignment from a single glance, in ten seconds, with nothing to turn.
func RoughAxis(f Frame, site Site, opt FitOptions) (Axis, bool) {
	if f.WidthPx <= 0 || f.HeightPx <= 0 || f.At.IsZero() {
		return Axis{}, false
	}
	alt, az := frameCentreDir(f, site, opt).altAz()
	return Axis{
		AltDeg: alt, AzDeg: az,
		// There is no circle here, so there is no radius, no arc and no residual to report. Saying one
		// frame rather than leaving it zero is what keeps the UI from rendering a blank measurement.
		Samples:     1,
		SigmaArcsec: roughSigmaArcsec,
		Warnings:    []string{WarnAssumedOnAxis},
	}, true
}

// SkyFromAltAz reports the catalogue coordinates a telescope aimed at a given altitude and azimuth is
// looking at — the inverse of what a plate solve tells you.
//
// Exported for the simulator and for tests that need to place a synthetic telescope somewhere specific
// relative to the pole. It is deliberately the mechanical direction going in: refraction is applied on
// the way out, so what comes back is what a solver would report, not a geometric abstraction.
func SkyFromAltAz(altDeg, azDeg float64, site Site, at time.Time, opt FitOptions) (raDeg, decDeg float64) {
	return skyFromDir(horizonVec(altDeg, azDeg), site, at, opt)
}
