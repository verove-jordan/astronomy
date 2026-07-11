package skyplan

import "time"

// Params are the inputs to a single planning run. Times are UTC; Location is only for formatting the
// local-time strings in the response.
type Params struct {
	At            time.Time
	Lat           float64
	Lon           float64
	ElevationM    float64
	SiteSQM       float64 // artificial+natural zenith sky brightness, mag/arcsec² (0 = unknown → no penalty)
	Optics        Optics
	Mode          string     // "" / "camera" (default) | "visual"
	Eyepieces     []Eyepiece // visual-mode kit; ignored in camera mode
	MinAltDeg     float64
	Twilight      string // "astro" | "nautical"
	Limit         int
	TypeFilter    string // optional: keep only this derived type
	CatalogFilter string // optional: keep only this source catalog
	Weights       Weights
	Location      *time.Location
}

// Weights are the tunable composite-score weights. The base sub-scores' weights sum to 1; the Moon
// score multiplies the weighted base.
type Weights struct {
	MaxAlt    float64
	DarkHours float64
	Framing   float64
	Detect    float64
	AltNow    float64
}

// DefaultWeights is the balanced default: altitude and dark-hours dominate, framing and detectability
// matter, current altitude is a tie-breaker — and the Moon multiplies the whole thing.
func DefaultWeights() Weights {
	return Weights{MaxAlt: 0.30, DarkHours: 0.25, Framing: 0.20, Detect: 0.15, AltNow: 0.10}
}

// VisualWeights tilts the balance toward "can my eye actually see it?" for the eyepiece mode:
// detectability matters as much as altitude, and the Moon bites harder via the stronger visual
// moon-sensitivity feeding the multiplier.
func VisualWeights() Weights {
	return Weights{MaxAlt: 0.25, DarkHours: 0.20, Framing: 0.20, Detect: 0.25, AltNow: 0.10}
}

// SubScores are the individual components, each in [0,1] where higher is better (the Moon and
// LightPollution components are multipliers on the weighted base: 1 = no interference).
type SubScores struct {
	MaxAlt         float64 `json:"max_alt"`
	AltNow         float64 `json:"alt_now"`
	DarkHours      float64 `json:"dark_hours"`
	Framing        float64 `json:"framing"`
	Detectability  float64 `json:"detectability"`
	Moon           float64 `json:"moon"`
	LightPollution float64 `json:"light_pollution"`
}

// Flags annotate per-target caveats for the UI.
type Flags struct {
	DetectabilityKnown bool `json:"detectability_known"`
	FramingKnown       bool `json:"framing_known"`
	Circumpolar        bool `json:"circumpolar"`
	Visible            bool `json:"visible"`
}

// AltSample is one point of the altitude-vs-time curve for the chart.
type AltSample struct {
	TMs    int64   `json:"t_ms"`
	AltDeg float64 `json:"alt_deg"`
}

// Target is one scored deep-sky object.
type Target struct {
	Name              string      `json:"name"`
	Aliases           []string    `json:"aliases,omitempty"`
	Catalog           string      `json:"catalog"`
	Type              string      `json:"type"`
	CommonName        string      `json:"common_name,omitempty"` // friendly name from OpenNGC ("Fireworks Galaxy")
	Morphology        string      `json:"morphology,omitempty"`  // Hubble class from OpenNGC (galaxies: "SABc", …)
	RADeg             float64     `json:"ra_deg"`
	DecDeg            float64     `json:"dec_deg"`
	AltNowDeg         float64     `json:"alt_now_deg"`
	AzNowDeg          float64     `json:"az_now_deg"`
	AirmassNow        float64     `json:"airmass_now"`
	MaxAltDeg         float64     `json:"max_alt_deg"`
	TransitUTCMs      int64       `json:"transit_utc_ms"`
	TransitLocal      string      `json:"transit_local"`
	DarkHoursAboveMin float64     `json:"dark_hours_above_min"`
	SizeArcmin        float64     `json:"size_arcmin"`
	SizeMinorArcmin   float64     `json:"size_minor_arcmin,omitempty"` // minor axis (OpenNGC) → true ellipse with SizeArcmin
	MagV              float64     `json:"mag_v"`
	SurfaceBrightness float64     `json:"surface_brightness"`
	FovFillPct        float64     `json:"fov_fill_pct"`
	MoonSepDeg        float64     `json:"moon_sep_deg"`
	Score             int         `json:"score"`
	SubScores         SubScores   `json:"subscores"`
	Flags             Flags       `json:"flags"`
	Reason            string      `json:"reason"`
	Composition       Composition `json:"composition"`
	AltSeries         []AltSample `json:"alt_series,omitempty"`
	// Visual (eyepiece) mode: the recommended eyepiece and the view it gives, set only when
	// Mode=="visual" and an eyepiece was chosen. omitempty keeps the camera-mode JSON byte-identical.
	ChosenEyepiece  string  `json:"chosen_eyepiece,omitempty"`
	EyepieceFocalMM float64 `json:"eyepiece_focal_mm,omitempty"`
	MagX            float64 `json:"mag_x,omitempty"`
	TrueFOVDeg      float64 `json:"true_fov_deg,omitempty"`
	ExitPupilMM     float64 `json:"exit_pupil_mm,omitempty"`
}

// MoonInfo summarizes the Moon for the response header (rise/set are 0/"" when there is no such event
// within tonight's chart window).
type MoonInfo struct {
	IllumFraction float64 `json:"illum_fraction"`
	AltNowDeg     float64 `json:"alt_now_deg"`
	UpNow         bool    `json:"up_now"`
	Phase         string  `json:"phase"`
	RiseUTCMs     int64   `json:"rise_utc_ms"`
	SetUTCMs      int64   `json:"set_utc_ms"`
	RiseLocal     string  `json:"rise_local"`
	SetLocal      string  `json:"set_local"`
}

// SunInfo carries tonight's sunset and sunrise (0/"" when the sun does not cross, e.g. polar summer).
type SunInfo struct {
	SetUTCMs  int64  `json:"set_utc_ms"`
	RiseUTCMs int64  `json:"rise_utc_ms"`
	SetLocal  string `json:"set_local"`
	RiseLocal string `json:"rise_local"`
}

// DarknessInfo summarizes tonight's dark window plus the Sun/Moon curves the night chart draws.
type DarknessInfo struct {
	Kind         string      `json:"kind"`
	NoAstroDark  bool        `json:"no_astro_dark"`
	DuskUTCMs    int64       `json:"dusk_utc_ms"`
	DawnUTCMs    int64       `json:"dawn_utc_ms"`
	DuskLocal    string      `json:"dusk_local"`
	DawnLocal    string      `json:"dawn_local"`
	DarkHours    float64     `json:"dark_hours"`
	NightStartMs int64       `json:"night_start_ms"` // chart window (≈ sunset−30m … sunrise+30m)
	NightEndMs   int64       `json:"night_end_ms"`
	Sun          SunInfo     `json:"sun"`
	Moon         MoonInfo    `json:"moon"`
	SunSeries    []AltSample `json:"sun_series"`
	MoonSeries   []AltSample `json:"moon_series"`
}

// Result is the planning output (the API wraps it with a query echo).
type Result struct {
	Darkness DarknessInfo `json:"darkness"`
	Count    int          `json:"count"`
	Targets  []Target     `json:"targets"`
	Warnings []string     `json:"warnings,omitempty"`
}
