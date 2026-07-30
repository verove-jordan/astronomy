// Package weather assembles astronomy-relevant weather for an observing site: a per-site hourly
// forecast (cloud cover, seeing, transparency, humidity, dew risk, wind, jet stream, instability,
// aerosols, aurora) for the /tonight panel + badge, and a regional cloud "cube" that the map animates.
//
// Sourcing is free + key-less and soft-failing, so the page always renders something:
//
//	Open-Meteo (forecast + air quality) + 7Timer! ASTRO (seeing/transparency) + NOAA SWPC (Kp)
//
// Weather is a forecast timeline, not a fixed-per-site quantity, so the clear-sky visibility scores
// never depend on it; the planner instead derives a SEPARATE per-target live score from the hourly
// verdicts (adapted into skyplan.WxSample by the API layer), so the stable ranking and the
// tonight-only conditions stay distinguishable. Any feed that is down is simply omitted (with a
// warning) — then targets carry no live score at all.
package weather

// SiteForecast is the per-site astronomy-weather timeline for tonight (the panel + the badge).
type SiteForecast struct {
	Lat      float64  `json:"lat"`
	Lon      float64  `json:"lon"`
	IssuedMs int64    `json:"issued_ms"`
	Hours    []Hour   `json:"hours"`
	Best     *Window  `json:"best,omitempty"` // best clear window inside the requested range
	Kp       *KpInfo  `json:"kp,omitempty"`   // geomagnetic activity / aurora (optional)
	Sources  []string `json:"sources"`        // which feeds contributed (attribution)
}

// Hour is one hourly sample. A zero value for a metric means "unknown" (the feed did not supply it).
type Hour struct {
	TMs          int64   `json:"t_ms"`
	CloudPct     float64 `json:"cloud_pct"`
	CloudLow     float64 `json:"cloud_low"`
	CloudMid     float64 `json:"cloud_mid"`
	CloudHigh    float64 `json:"cloud_high"`
	SeeingArcsec float64 `json:"seeing_arcsec"` // 0 = unknown
	Transparency float64 `json:"transparency"`  // 0..1 (1 = pristine); 0 = unknown
	HumidityPct  float64 `json:"humidity_pct"`
	DewPointC    float64 `json:"dew_point_c"`
	TempC        float64 `json:"temp_c"`
	DewSpreadC   float64 `json:"dew_spread_c"`
	DewRisk      string  `json:"dew_risk"` // "low" | "moderate" | "high"
	WindKmh      float64 `json:"wind_kmh"`
	GustKmh      float64 `json:"gust_kmh"`
	Jet300Kmh    float64 `json:"jet300_kmh"`
	CAPE         float64 `json:"cape"`
	LiftedIndex  float64 `json:"lifted_index"`
	VisibilityM  float64 `json:"visibility_m"`
	PrecipPct    float64 `json:"precip_pct"`
	AOD          float64 `json:"aod"`     // aerosol optical depth @550nm (0 = unknown)
	Verdict      float64 `json:"verdict"` // 0..100 overall observability for this hour
}

// Window is a contiguous time span — used for the best clear window inside the dark window.
type Window struct {
	StartMs int64   `json:"start_ms"`
	EndMs   int64   `json:"end_ms"`
	Verdict float64 `json:"verdict"` // mean verdict over the span
}

// KpInfo is geomagnetic activity (aurora potential): the latest observed Kp + the max over the window.
type KpInfo struct {
	Now      float64 `json:"now"`
	Max      float64 `json:"max"`
	Aurora   string  `json:"aurora"` // "unlikely" | "possible" | "likely" at this latitude
	IssuedMs int64   `json:"issued_ms"`
}

// Grid is the animated cloud cube: a regular lat/lon grid over a bbox, one value per cell per timestep.
// Layers maps a metric name (e.g. "clouds") to [timestepIndex][cellIndex] in row-major order (Ny rows
// of Nx columns, north-to-south, west-to-east), values in the metric's natural units (cloud = %).
type Grid struct {
	BBox      [4]float64             `json:"bbox"` // [west, south, east, north]
	Nx        int                    `json:"nx"`
	Ny        int                    `json:"ny"`
	Timesteps []int64                `json:"timesteps"` // epoch ms per frame
	Layers    map[string][][]float32 `json:"layers"`
	IssuedMs  int64                  `json:"issued_ms"`
}
