// Package polaralign is the geometry of getting a mount's right-ascension axis onto the celestial pole,
// by either of the two routes an observer has.
//
// The first is open loop, and lives in this file: given a site and a time, work out where the pole star
// ought to sit on a polar-scope reticle — its hour angle, and the clock position to turn the reticle to
// before dropping the star into the bubble. It tells you where to put the star; it cannot tell you how
// far off you ended up.
//
// The second closes the loop with the camera, and lives in the other files here. Turning the telescope
// about its right-ascension axis sweeps the optical axis around a circle centred on that axis, so a
// handful of plate-solved frames along the sweep locate the axis itself (axis.go), which reduces to two
// numbers and two directions for the two adjusting bolts (correct.go), which becomes a marker to drive
// into the crosshairs on the live image (target.go, adjust.go). It needs no pointing model, no
// encoders, and no connection to the mount at all — which is what lets the rotation between frames be
// done by hand, on any mount ever made.
//
// Everything here is pure: no I/O, no clock of its own, nothing but internal/astro and a WCS.
package polaralign

import (
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// minUsableLatDeg is the latitude below which the celestial pole sits too low in the horizon murk for a
// polar scope to be practical; the UI surfaces a warning rather than refusing to compute.
const minUsableLatDeg = 10.0

// Result is the polar-alignment readout for an observer at a site and instant. PositionAngleDeg/ClockHour
// are for an inverting straight-through polar scope (the default); the frontend applies the erect/mirror
// display transforms.
type Result struct {
	Hemisphere       string  `json:"hemisphere"`         // "north" | "south"
	PoleStarName     string  `json:"pole_star_name"`     // "Polaris" | "σ Octantis"
	PoleStarRADeg    float64 `json:"pole_star_ra_deg"`   // precessed to the epoch of the request
	PoleStarDecDeg   float64 `json:"pole_star_dec_deg"`  //
	HADeg            float64 `json:"ha_deg"`             // local hour angle, west-positive, [0,360)
	PositionAngleDeg float64 `json:"position_angle_deg"` // reticle angle, clockwise from 12 o'clock
	ClockHour        float64 `json:"clock_hour"`         // PositionAngleDeg / 30, in [0,12)
	SeparationDeg    float64 `json:"separation_deg"`     // pole star → true celestial pole (~0.7° / ~1°)
	AltDeg           float64 `json:"alt_deg"`            // pole-star altitude (≈ |latitude|)
	AzDeg            float64 `json:"az_deg"`             //
	LSTDeg           float64 `json:"lst_deg"`            //
	PoleStarVisible  bool    `json:"pole_star_visible"`  // apparent altitude above the horizon
	LatTooLow        bool    `json:"lat_too_low"`        // |lat| < minUsableLatDeg
}

// Compute returns the polar-alignment readout for an observer at lat/lon at instant t.
func Compute(t time.Time, lat, lon float64) Result {
	north := lat >= 0
	ra, dec, name := astro.PoleStar(north, t)
	lst := astro.LST(t, lon)
	ha := norm360(lst - ra) // west-positive hour angle of the pole star

	// Facing north the sky rotates counter-clockwise, facing south clockwise — hence the sign flip. An
	// inverting polar scope rotates the field 180°, so at upper culmination (HA 0) the star appears at
	// the 6 o'clock position. PositionAngle is measured clockwise from 12 o'clock.
	sign := -1.0
	poleDec := 90.0
	if !north {
		sign = 1.0
		poleDec = -90.0
	}
	pa := norm360(180 + sign*ha)

	alt, az := astro.Horizontal(ra, dec, lat, lon, t)

	return Result{
		Hemisphere:       hemisphere(north),
		PoleStarName:     name,
		PoleStarRADeg:    round(ra, 4),
		PoleStarDecDeg:   round(dec, 4),
		HADeg:            round(ha, 2),
		PositionAngleDeg: round(pa, 2),
		ClockHour:        round(pa/30, 3),
		SeparationDeg:    round(astro.AngularSeparation(ra, dec, 0, poleDec), 4),
		AltDeg:           round(alt, 2),
		AzDeg:            round(az, 2),
		LSTDeg:           round(lst, 2),
		PoleStarVisible:  astro.ApparentAltitude(alt) > 0,
		LatTooLow:        math.Abs(lat) < minUsableLatDeg,
	}
}

func hemisphere(north bool) string {
	if north {
		return "north"
	}
	return "south"
}

// norm360 wraps an angle into [0,360) (astro's helper is unexported, so we keep a local copy).
func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
