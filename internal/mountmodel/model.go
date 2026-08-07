// Package mountmodel is the telescope's pointing model: the map between the mount's two shaft
// angles and a place in the sky, and the fit that improves it every time a star is centred.
//
// It exists because a Celestron hand controller will not accept an alignment over the serial line —
// there is no such command, and a sync is accepted without ever setting the aligned flag (measured on
// an AVX, firmware 5.31). A mount driven from software therefore cannot borrow the hand controller's
// model; it has to keep its own. The motor controllers make that possible: they report shaft angles
// whether or not anything has been aligned, so the app can measure where the telescope really is and
// work out the rest itself.
//
// Two layers, deliberately separate:
//
//   - Geometry is the ideal mount — a perfectly polar-aligned German equatorial whose shafts read
//     hour angle and declination directly, offset by where its zeros happen to sit. It is exact, has
//     no fitted parameters, and is what makes the first slew of the night possible from the home
//     position alone.
//   - Model is the corrections on top: the handful of small angles that describe how a real mount
//     differs from that ideal. They are fitted from centred stars, and they are what turns "roughly
//     the right part of the sky" into "in the frame".
//
// Nothing here talks to hardware or moves anything; it is pure arithmetic over angles, which is what
// lets the whole thing be tested against a synthetic mount whose errors are known exactly.
package mountmodel

import (
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// PierSide names which side of the mount the telescope is on. A German equatorial can reach almost
// every point in the sky two ways — shaft angles (a, d) and (a+180°, −d) put the optics in the same
// place — and the two differ by the tube being east or west of the pier.
type PierSide string

const (
	// PierEast is the tube east of the pier, which is how a mount points at the WESTERN sky.
	PierEast PierSide = "east"
	// PierWest is the tube west of the pier, pointing at the eastern sky.
	PierWest PierSide = "west"
)

// Geometry is the ideal polar-aligned equatorial: where each shaft reads zero, and which way its
// numbers run. Everything here is mechanical and knowable without a single star.
type Geometry struct {
	// RAZeroDeg is the RA shaft angle at which the telescope points at the meridian (hour angle 0)
	// with the tube on the west side of the pier.
	RAZeroDeg float64 `json:"ra_zero_deg"`
	// DecZeroDeg is the declination shaft angle at which the telescope points at the celestial pole.
	// On a mount parked at its home index this is simply the shaft angle as parked, which is why the
	// home position is worth so much: it hands over one of the two zeros for free.
	DecZeroDeg float64 `json:"dec_zero_deg"`
	// RASense is +1 when the RA shaft angle grows with hour angle, −1 when it runs backwards.
	//
	// Do not guess this. It is measurable in thirty seconds and only in one way: leave the mount
	// tracking and watch the shaft. Tracking exists to hold a star while its hour angle increases, so
	// a tracking mount's RA shaft moves at exactly sidereal in the direction hour angle runs. On the
	// AVX it measured +15.040″/s against sidereal's 15.041, so RASense is +1 there.
	RASense float64 `json:"ra_sense"`
	// DecSense is +1 when moving the declination shaft away from its pole zero lowers declination.
	// Unlike RASense there is no way to read this off a stationary mount — it takes one star.
	DecSense float64 `json:"dec_sense"`
	// LatDeg and LonDeg are the observing site, needed to turn hour angle into right ascension.
	LatDeg float64 `json:"lat_deg"`
	LonDeg float64 `json:"lon_deg"`
}

// DefaultGeometry is a mount parked at its home index: counterweight down, tube on the pole. Both
// zeros are read straight off the parked shafts, so a mount that starts the night properly parked
// begins with a usable model before any star has been looked at.
func DefaultGeometry(parkedRADeg, parkedDecDeg, latDeg, lonDeg float64) Geometry {
	return Geometry{
		// At the home index the tube points at the pole, and the pier-side convention puts the
		// counterweight down — a quarter turn from the meridian.
		RAZeroDeg:  parkedRADeg - 90,
		DecZeroDeg: parkedDecDeg,
		RASense:    1,
		DecSense:   1,
		LatDeg:     latDeg,
		LonDeg:     lonDeg,
	}
}

// Observation is one centred star: what the shafts read at the moment the star sat in the middle of
// the frame, and where that star actually was. The pair is the entire input to the fit — everything
// the model knows about this mount comes from a stack of these.
type Observation struct {
	// ShaftRADeg and ShaftDecDeg are the raw motor-controller readings.
	ShaftRADeg  float64 `json:"shaft_ra_deg"`
	ShaftDecDeg float64 `json:"shaft_dec_deg"`
	// RADeg and DecDeg are the star's true position, J2000.
	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	// At is when it was centred. Hour angle moves a quarter of a degree a minute, so an observation
	// without its timestamp is worthless.
	At time.Time `json:"at"`
	// Star is the name shown in the UI; the fit ignores it.
	Star string `json:"star,omitempty"`
	// HCName is the label this star carries in the hand controller's own list, kept so the panel can
	// go on calling a star what the mount calls it.
	HCName string `json:"hc_name,omitempty"`
}

// idealFromShafts converts shaft angles to the hour angle and declination an ideal mount would be
// pointing at, and says which side of the pier it is on.
//
// The declination shaft measures the angle away from the pole, so declination is 90° minus it. Past
// the pole the tube has swung through to the other side: the arithmetic gives a declination above
// 90°, which is not a place, and the fix is the flip — reflect back under the pole and add half a
// turn of hour angle.
func (g Geometry) idealFromShafts(shaftRADeg, shaftDecDeg float64) (haDeg, decDeg float64, side PierSide) {
	d := g.DecSense * normalizeSigned(shaftDecDeg-g.DecZeroDeg)
	h := g.RASense * (shaftRADeg - g.RAZeroDeg)
	dec := 90 - d
	side = PierWest
	if dec > 90 || dec < -90 {
		// Through the pole: the same optics, reached the other way round.
		dec = math.Copysign(180, dec) - dec
		h += 180
		side = PierEast
	}
	return normalizeSigned(h), dec, side
}

// idealShaftsFor is idealFromShafts backwards: the shaft angles that would put an ideal mount on this
// hour angle and declination, on the requested side of the pier.
func (g Geometry) idealShaftsFor(haDeg, decDeg float64, side PierSide) (shaftRADeg, shaftDecDeg float64) {
	d := 90 - decDeg
	h := haDeg
	if side == PierEast {
		d = -d
		h += 180
	}
	shaftRADeg = normalize360(g.RAZeroDeg + h/g.RASense)
	shaftDecDeg = normalize360(g.DecZeroDeg + d/g.DecSense)
	return shaftRADeg, shaftDecDeg
}

// Model is a Geometry plus the fitted corrections, and is the thing the rest of the app points with.
type Model struct {
	Geometry Geometry `json:"geometry"`
	Terms    Terms    `json:"terms"`
	// Observations is how many centred stars the terms were fitted from.
	Observations int `json:"observations"`
	// RMSArcsec is the root-mean-square pointing residual over those stars, and the only honest
	// summary of how well the mount points. It is a fit residual, not a prediction: it says how well
	// the model explains the stars it has seen, and a model fitted from two stars will flatter itself.
	RMSArcsec float64 `json:"rms_arcsec"`
	// WorstArcsec is the largest single residual — a star fumbled during centring shows up here long
	// before it moves the RMS.
	WorstArcsec float64 `json:"worst_arcsec"`
}

// Forward reports where the telescope is actually pointing, given what the shafts read.
func (m Model) Forward(shaftRADeg, shaftDecDeg float64, at time.Time) (raDeg, decDeg float64, side PierSide) {
	ha, dec, side := m.Geometry.idealFromShafts(shaftRADeg, shaftDecDeg)
	dHA, dDec := m.Terms.correction(ha, dec)
	ha, dec = ha+dHA, dec+dDec
	// Hour angle is measured west of the meridian, so right ascension is sidereal time minus it.
	ra := normalize360(astro.LST(at, m.Geometry.LonDeg) - ha)
	return ra, clampDec(dec), side
}

// Inverse is the pointing question that matters: which shaft angles put this star in the middle of
// the frame? The corrections are defined on the sky side, so they are removed by one round of
// substitution — the terms are small angles and the map is smooth, so a single pass converges far
// below the arcsecond the encoders can resolve.
func (m Model) Inverse(raDeg, decDeg float64, at time.Time, side PierSide) (shaftRADeg, shaftDecDeg float64) {
	ha := normalizeSigned(astro.LST(at, m.Geometry.LonDeg) - raDeg)
	dHA, dDec := m.Terms.correction(ha, decDeg)
	return m.Geometry.idealShaftsFor(normalizeSigned(ha-dHA), clampDec(decDeg-dDec), side)
}

// NaturalSide is the side of the pier a German equatorial should be on to reach this hour angle:
// the tube goes east to look west, and west to look east. Choosing the other one is what runs a
// counterweight into a tripod leg.
func NaturalSide(haDeg float64) PierSide {
	if normalizeSigned(haDeg) >= 0 {
		return PierEast // west of the meridian
	}
	return PierWest
}

// residual is how far a model's prediction lands from where the star really was, in arcseconds of
// true angle on the sky. The hour-angle part is scaled by cos(dec) because a degree of hour angle is
// a smaller distance the closer to the pole you look — without it, Polaris would appear to be
// pointed at appallingly and every fit would chase it.
func (m Model) residual(o Observation) float64 {
	ra, dec, _ := m.Forward(o.ShaftRADeg, o.ShaftDecDeg, o.At)
	trueRA, trueDec := astro.PrecessFromJ2000(o.RADeg, o.DecDeg, o.At)
	dRA := normalizeSigned(ra-trueRA) * math.Cos(rad(trueDec))
	dDec := dec - trueDec
	return math.Hypot(dRA, dDec) * 3600
}

// Residuals reports the per-observation pointing error, in the order given.
func (m Model) Residuals(obs []Observation) []float64 {
	out := make([]float64, len(obs))
	for i, o := range obs {
		out[i] = m.residual(o)
	}
	return out
}

// String renders the model for logs and the agent's prompts.
func (m Model) String() string {
	return fmt.Sprintf("pointing model: %d stars, RMS %.0f″, worst %.0f″ (%s)",
		m.Observations, m.RMSArcsec, m.WorstArcsec, m.Terms)
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }
func deg(r float64) float64   { return r * 180 / math.Pi }

// normalize360 folds an angle into [0, 360).
func normalize360(a float64) float64 {
	a = math.Mod(a, 360)
	if a < 0 {
		a += 360
	}
	return a
}

// normalizeSigned folds an angle into (−180, 180], which is the range hour angles and small errors
// belong in — the seam sits behind the observer instead of on the meridian.
func normalizeSigned(a float64) float64 {
	a = normalize360(a)
	if a > 180 {
		a -= 360
	}
	return a
}

// clampDec keeps a declination a declination. Corrections are applied by addition and a star a few
// arcseconds from the pole can be pushed over it; 90.001° is not a coordinate.
func clampDec(d float64) float64 {
	return math.Max(-90, math.Min(90, d))
}
