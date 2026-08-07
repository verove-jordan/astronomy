// Package skylog records the conditions a capture session actually ran under.
//
// It exists because the weather the engine can see is a FORECAST, not an archive. internal/weather
// asks Open-Meteo for past_days=1/forecast_days=2, 7Timer and NOAA SWPC are recent-or-forward only,
// and there is no history endpoint anywhere in the engine — so a night's conditions are retrievable
// for about a day and then gone forever. The only honest record is one written while the session is
// still running, which is what this package does.
//
// The shape is deliberately the same as internal/capture: the sampler talks to two narrow interfaces
// so the package imports neither the database nor an HTTP client, and a whole night can be replayed
// against fakes in a unit test.
//
// Everything here soft-fails. A feed that is down produces a row with zeroed weather and Source
// "unavailable" — never a missing row, and never an error that reaches the sequencer. The ephemeris
// half (Moon, target altitude, airmass) is computed locally from internal/astro and stays correct on
// a night when nothing else works.
package skylog

import (
	"context"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// Where a sample's weather came from, so an all-zero row reads as "the feed was down" rather than as
// "the sky was flawless and perfectly still".
const (
	SourceLive        = "live"        // freshly fetched from upstream
	SourceCached      = "cached"      // served from the provider's cache (possibly its stale grace)
	SourceUnavailable = "unavailable" // no hourly data at all
)

// liveWindow is how recent a forecast must be to count as freshly fetched. weather.SiteForecast sets
// IssuedMs at assembly time, so `now - IssuedMs` is exactly the provider's cache age.
const liveWindow = 60 * time.Second

// hourTolerance is how far a sample may sit from the nearest forecast hour before the weather is
// treated as missing. The feeds are hourly, so a healthy timeline is never more than 30 min away;
// anything beyond this means a gap, and inventing a value across it would be a lie.
const hourTolerance = 90 * time.Minute

// maxAirmass caps the recorded airmass. astro.Airmass is +Inf below the horizon and rises steeply at
// the limb; +Inf cannot be stored in a DOUBLE PRECISION column and cannot be JSON-marshalled, so it
// is clamped here rather than blowing up a night's record at write time.
const maxAirmass = 40

// Source is the read side: the weather and light-pollution providers, behind an interface so this
// package never imports them concretely and tests need no network. Both methods return a value plus
// a warning string and never an error, exactly as the real providers do.
type Source interface {
	Forecast(ctx context.Context, lat, lon float64) (weather.SiteForecast, string)
	Site(ctx context.Context, lat, lon float64) (lightpollution.SiteQuality, string)
}

// Sink is the write side, so this package never imports internal/store.
type Sink interface {
	AddSample(ctx context.Context, sessionID int64, s Sample) error
	SaveSummary(ctx context.Context, sessionID int64, sum Summary) error
	SaveForecast(ctx context.Context, sessionID int64, kind string, atMs int64, f weather.SiteForecast) error
}

// Site is where the telescope stood.
type Site struct {
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	ElevationM float64 `json:"elevation_m"`
}

// Target is where it pointed (J2000). Valid is false for a session that carried no coordinates —
// which is what keeps a zeroed MoonSepDeg distinguishable from a genuine one.
type Target struct {
	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	Valid  bool    `json:"valid"`
}

// Sample is one hourly observation of the sky over a running session. The JSON names match the
// capture_conditions columns so the API layer can hand rows straight to the browser.
type Sample struct {
	AtMs          int64  `json:"at_ms"`
	SessionStatus string `json:"session_status"`

	// weather.Hour, flattened. Zero means "the feed did not supply it" — the weather package's own
	// contract, preserved rather than turned into a null so readers need only one rule.
	CloudPct     float64 `json:"cloud_pct"`
	CloudLow     float64 `json:"cloud_low"`
	CloudMid     float64 `json:"cloud_mid"`
	CloudHigh    float64 `json:"cloud_high"`
	SeeingArcsec float64 `json:"seeing_arcsec"`
	Transparency float64 `json:"transparency"`
	HumidityPct  float64 `json:"humidity_pct"`
	DewPointC    float64 `json:"dew_point_c"`
	TempC        float64 `json:"temp_c"`
	DewSpreadC   float64 `json:"dew_spread_c"`
	DewRisk      string  `json:"dew_risk"`
	WindKmh      float64 `json:"wind_kmh"`
	GustKmh      float64 `json:"gust_kmh"`
	Jet300Kmh    float64 `json:"jet300_kmh"`
	CAPE         float64 `json:"cape"`
	LiftedIndex  float64 `json:"lifted_index"`
	VisibilityM  float64 `json:"visibility_m"`
	PrecipPct    float64 `json:"precip_pct"`
	AOD          float64 `json:"aod"`
	Verdict      float64 `json:"verdict"`
	KpNow        float64 `json:"kp_now"`
	KpMax        float64 `json:"kp_max"`
	Aurora       string  `json:"aurora"`

	// Computed locally, never fetched.
	MoonIllum         float64 `json:"moon_illum"`
	MoonAltDeg        float64 `json:"moon_alt_deg"`
	MoonAzDeg         float64 `json:"moon_az_deg"`
	MoonPhaseAngleDeg float64 `json:"moon_phase_angle_deg"`
	MoonSepDeg        float64 `json:"moon_sep_deg"`
	TargetAltDeg      float64 `json:"target_alt_deg"`
	TargetAzDeg       float64 `json:"target_az_deg"`
	TargetAirmass     float64 `json:"target_airmass"`
	TargetValid       bool    `json:"target_valid"`

	SQM    float64 `json:"sqm"`
	Bortle int     `json:"bortle"`

	ForecastAgeMs int64  `json:"forecast_age_ms"`
	Source        string `json:"source"`
}

// Observe builds one sample. It is pure: every input is a parameter, including the instant, so the
// whole computation is exercisable from a table test with no clock and no network.
//
// The caller fills in SessionStatus — this function has no notion of a session.
func Observe(t time.Time, site Site, tgt Target, f weather.SiteForecast, q lightpollution.SiteQuality) Sample {
	s := Sample{AtMs: t.UnixMilli(), Source: SourceUnavailable}

	if h, ok := hourAt(f, t); ok {
		s.CloudPct, s.CloudLow, s.CloudMid, s.CloudHigh = h.CloudPct, h.CloudLow, h.CloudMid, h.CloudHigh
		s.SeeingArcsec, s.Transparency = h.SeeingArcsec, h.Transparency
		s.HumidityPct, s.DewPointC, s.TempC, s.DewSpreadC = h.HumidityPct, h.DewPointC, h.TempC, h.DewSpreadC
		s.DewRisk = h.DewRisk
		s.WindKmh, s.GustKmh, s.Jet300Kmh = h.WindKmh, h.GustKmh, h.Jet300Kmh
		s.CAPE, s.LiftedIndex, s.VisibilityM = h.CAPE, h.LiftedIndex, h.VisibilityM
		s.PrecipPct, s.AOD, s.Verdict = h.PrecipPct, h.AOD, h.Verdict

		age := t.Sub(time.UnixMilli(f.IssuedMs))
		if age < 0 {
			age = 0 // a forecast issued "after" this sample is a clock skew, not a negative age
		}
		s.ForecastAgeMs = age.Milliseconds()
		s.Source = SourceCached
		if age <= liveWindow {
			s.Source = SourceLive
		}
	}
	if f.Kp != nil {
		s.KpNow, s.KpMax, s.Aurora = f.Kp.Now, f.Kp.Max, f.Kp.Aurora
	}

	moon := astro.MoonNow(t, site.Lat, site.Lon)
	s.MoonIllum, s.MoonAltDeg, s.MoonAzDeg = moon.IllumFraction, moon.AltDeg, moon.AzDeg
	s.MoonPhaseAngleDeg = astro.MoonPhaseAngle(t)

	if tgt.Valid {
		s.TargetValid = true
		alt, az := astro.Horizontal(tgt.RADeg, tgt.DecDeg, site.Lat, site.Lon, t)
		s.TargetAltDeg, s.TargetAzDeg = alt, az
		s.TargetAirmass = airmassAt(alt)
		s.MoonSepDeg = astro.AngularSeparation(moon.RADeg, moon.DecDeg, tgt.RADeg, tgt.DecDeg)
	}

	s.SQM, s.Bortle = q.SQM, q.Bortle
	return s
}

// airmassAt is astro.Airmass made storable: 0 below the horizon (where the true value is +Inf and
// meaningless anyway) and clamped at the limb, so no row can carry a value Postgres or encoding/json
// would choke on.
func airmassAt(trueAltDeg float64) float64 {
	app := astro.ApparentAltitude(trueAltDeg)
	if app < 0 {
		return 0
	}
	x := astro.Airmass(app)
	if math.IsInf(x, 0) || math.IsNaN(x) || x > maxAirmass {
		return maxAirmass
	}
	return x
}

// hourAt picks the forecast hour nearest t, or reports that the timeline does not cover it. This is
// the only place that knows the feeds are hourly.
func hourAt(f weather.SiteForecast, t time.Time) (weather.Hour, bool) {
	best, bestGap := weather.Hour{}, time.Duration(math.MaxInt64)
	for _, h := range f.Hours {
		gap := t.Sub(time.UnixMilli(h.TMs))
		if gap < 0 {
			gap = -gap
		}
		if gap < bestGap {
			best, bestGap = h, gap
		}
	}
	if bestGap > hourTolerance {
		return weather.Hour{}, false
	}
	return best, true
}
