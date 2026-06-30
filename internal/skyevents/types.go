package skyevents

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// Category gates which event families are generated.
type Category string

const (
	CatEclipse   Category = "eclipse"
	CatPlanet    Category = "planet"    // conjunctions, oppositions, elongations, planet–Moon
	CatMeteor    Category = "meteor"    // meteor-shower peaks
	CatMoon      Category = "moon"      // phases + supermoon
	CatSeason    Category = "season"    // equinoxes/solstices + Earth perihelion/aphelion
	CatComet     Category = "comet"     // bright comets (online MPC feed)
	CatSatellite Category = "satellite" // ISS/bright-sat transits of the Sun/Moon (online TLEs)
)

// Params are the inputs to a calendar computation. Times are UTC; Location formats nothing here (the
// API attaches local strings) but is passed to skyplan.NightContext for the per-event chart.
type Params struct {
	From, To   time.Time
	Lat, Lon   float64
	ElevationM float64
	SiteSQM    float64 // zenith sky brightness, mag/arcsec² (0 = unknown → no light-pollution penalty)
	Optics     skyplan.Optics
	Twilight   string // "astro" | "nautical"
	Categories map[Category]bool
	Location   *time.Location
}

// enabled reports whether a category should be generated (an empty set enables everything).
func (p Params) enabled(c Category) bool {
	if len(p.Categories) == 0 {
		return true
	}
	return p.Categories[c]
}

// Visibility scores how observable/spectacular an event is per instrument, each 0..100. Telescope uses
// the user's configured aperture/FOV.
type Visibility struct {
	NakedEye  int `json:"naked_eye"`
	Binocular int `json:"binocular"`
	Telescope int `json:"telescope"`
}

// ScoreFactor is one contributor to an event's score, surfaced in the "why this score" UI. Weight is
// 0..1 (higher = more favourable); Detail is a locale-neutral value (e.g. "42°", "mag 5.8", "100% · 8°").
type ScoreFactor struct {
	Key    string  `json:"key"` // rarity | altitude | sun_up | moon | brightness
	Weight float64 `json:"weight"`
	Detail string  `json:"detail,omitempty"`
}

// Contact is a named timing point inside an extended event (eclipse phases, transit ingress/egress).
type Contact struct {
	Label  string  `json:"label"`
	UTCMs  int64   `json:"utc_ms"`
	AltDeg float64 `json:"alt_deg"`
}

// Event is one scored astronomical event. The backend emits structured fields + Kind; the frontend
// composes the localized title. Times are UTC epoch-ms.
type Event struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Subtype    string `json:"subtype,omitempty"` // eclipse type, shower code, phase name, "sun"/"moon" for transits
	PeakUTCMs  int64  `json:"peak_utc_ms"`
	StartUTCMs int64  `json:"start_utc_ms,omitempty"`
	EndUTCMs   int64  `json:"end_utc_ms,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`

	Contacts []Contact `json:"contacts,omitempty"`
	Bodies   []string  `json:"bodies,omitempty"` // canonical lowercase keys, UI localizes

	Title         string  `json:"title"` // English fallback
	ExtraText     string  `json:"extra_text,omitempty"`
	SeparationDeg float64 `json:"separation_deg,omitempty"`
	Magnitude     float64 `json:"magnitude,omitempty"`
	HasMag        bool    `json:"has_mag"`
	ZHR           float64 `json:"zhr,omitempty"`

	Score        int           `json:"score"`
	Visibility   Visibility    `json:"visibility"`
	ScoreFactors []ScoreFactor `json:"score_factors,omitempty"`
	Instrument   string        `json:"instrument"` // best tier reached: naked_eye|binocular|telescope|none
	Notable      bool          `json:"notable"`
	Reason       string        `json:"reason"`

	RADeg        float64 `json:"ra_deg,omitempty"`
	DecDeg       float64 `json:"dec_deg,omitempty"`
	HasPosition  bool    `json:"has_position"`
	AltAtBestDeg float64 `json:"alt_at_best_deg"`
	AzAtBestDeg  float64 `json:"az_at_best_deg,omitempty"`
	BestUTCMs    int64   `json:"best_utc_ms,omitempty"`
	MoonIllum    float64 `json:"moon_illum"`
	MoonSepDeg   float64 `json:"moon_sep_deg,omitempty"`
	InPath       *bool   `json:"in_path,omitempty"` // satellite transit: site inside the ground path

	Night *skyplan.DarknessInfo `json:"night,omitempty"` // per-event chart context (timed nighttime events)
}

// Result is the calendar output (the API wraps it with a query echo + date range).
type Result struct {
	Events   []Event  `json:"events"`
	Count    int      `json:"count"`
	Warnings []string `json:"warnings,omitempty"`
}
