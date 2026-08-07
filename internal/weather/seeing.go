package weather

import "math"

// Where a seeing figure came from. Recorded per hour so the UI can be honest about it.
const (
	SeeingSourceDerived    = "derived"
	SeeingSourceSevenTimer = "7timer"
)

// The derived seeing index maps atmospheric turbulence onto an arcsecond FWHM. Astronomical seeing is
// dominated by two layers: the tropopause jet, whose wind shear generates free-atmosphere turbulence,
// and the boundary layer, whose ground-driven convection blurs the first few hundred metres. Neither
// is a forecast product on any free feed, but both are computable from pressure-level winds, which
// Open-Meteo does serve hourly at model resolution — far finer than 7Timer's 3-hourly 10 km GFS grid.
//
// The scales below are the wind speeds at which each term contributes about one unit of turbulence;
// the weights are their relative share. Together they put a still, flat-profile night near 1.0" and a
// night with a 200 km/h jet overhead near 5".
const (
	seeingFloorArcsec = 0.6 // the best seeing this model will ever claim
	seeingSpanArcsec  = 4.4 // floor+span = the worst

	jetScaleKmh         = 100.0
	upperShearScaleKmh  = 50.0
	lowerShearScaleKmh  = 40.0
	surfaceWindScaleKmh = 25.0

	jetWeight        = 0.25
	upperShearWeight = 0.35
	lowerShearWeight = 0.15
	surfaceWeight    = 0.25

	// blReferenceM is a typical night-time boundary-layer depth. A deep, convective layer stirs more
	// air above the telescope; a shallow one under an inversion is the classic steady-seeing night.
	blReferenceM  = 800.0
	blFactorMin   = 0.4
	blFactorMax   = 1.6
	seeingWorstKm = 400.0 // sanity ceiling on any single wind reading (km/h)
)

// shear is one hour's wind profile — the input to the derived seeing index.
type shear struct {
	jetKmh     float64 // 300 hPa wind: close enough to the jet core, and already fetched for the panel
	w500Kmh    float64
	w850Kmh    float64
	surfaceKmh float64
	blHeightM  float64 // 0 = unknown
}

// derivedSeeing estimates seeing FWHM in arcseconds from a wind profile, or 0 when the profile is too
// incomplete to say anything (no jet level, or no mid-level to shear against).
func derivedSeeing(s shear) float64 {
	if s.jetKmh <= 0 || s.jetKmh > seeingWorstKm || s.w500Kmh <= 0 {
		return 0
	}

	turbulence := jetWeight * square(s.jetKmh/jetScaleKmh)
	turbulence += upperShearWeight * square(math.Abs(s.jetKmh-s.w500Kmh)/upperShearScaleKmh)
	if s.w850Kmh > 0 {
		turbulence += lowerShearWeight * square(math.Abs(s.w500Kmh-s.w850Kmh)/lowerShearScaleKmh)
	}
	if s.surfaceKmh > 0 {
		ground := math.Pow(s.surfaceKmh/surfaceWindScaleKmh, 1.5)
		turbulence += surfaceWeight * ground * boundaryLayerFactor(s.blHeightM)
	}

	return round2(seeingFloorArcsec + seeingSpanArcsec*(1-math.Exp(-turbulence)))
}

// boundaryLayerFactor scales the ground-turbulence term by how deep the mixed layer is, relative to a
// typical night. Unknown depth is neutral.
func boundaryLayerFactor(heightM float64) float64 {
	if heightM <= 0 {
		return 1
	}
	return clampf(heightM/blReferenceM, blFactorMin, blFactorMax)
}

func square(x float64) float64 { return x * x }
