package skylog

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

var (
	testSite = Site{Lat: 48.8566, Lon: 2.3522, ElevationM: 35}
	testTime = time.Date(2026, 8, 2, 22, 30, 0, 0, time.UTC)
)

func forecastAt(base time.Time, n int) weather.SiteForecast {
	f := weather.SiteForecast{IssuedMs: base.UnixMilli()}
	for i := 0; i < n; i++ {
		f.Hours = append(f.Hours, weather.Hour{
			TMs:          base.Add(time.Duration(i) * time.Hour).UnixMilli(),
			CloudPct:     float64(10 * i),
			SeeingArcsec: 2.5,
			Transparency: 0.8,
			TempC:        12,
			Verdict:      70,
		})
	}
	return f
}

func TestHourAt(t *testing.T) {
	base := testTime.Truncate(time.Hour)
	f := forecastAt(base, 3)

	cases := []struct {
		name     string
		at       time.Time
		wantOK   bool
		wantTMs  int64
		forecast weather.SiteForecast
	}{
		{"exact hour", base, true, base.UnixMilli(), f},
		{"nearest is the next hour", base.Add(70 * time.Minute), true, base.Add(time.Hour).UnixMilli(), f},
		{"nearest is the previous hour", base.Add(50 * time.Minute), true, base.Add(time.Hour).UnixMilli(), f},
		{"before the timeline but inside tolerance", base.Add(-45 * time.Minute), true, base.UnixMilli(), f},
		{"far past the timeline", base.Add(9 * time.Hour), false, 0, f},
		{"far before the timeline", base.Add(-9 * time.Hour), false, 0, f},
		{"empty timeline", base, false, 0, weather.SiteForecast{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ok := hourAt(tc.forecast, tc.at)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantTMs, h.TMs)
			}
		})
	}
}

func TestAirmassAt(t *testing.T) {
	cases := []struct {
		name   string
		altDeg float64
		want   float64
		delta  float64
	}{
		{"zenith", 90, 1.0, 0.01},
		{"thirty degrees", 30, 1.995, 0.05},
		{"ten degrees", 10, 5.6, 0.3},
		{"below the horizon reads as unknown", -5, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := airmassAt(tc.altDeg)
			assert.False(t, math.IsInf(got, 0) || math.IsNaN(got), "must be storable")
			assert.InDelta(t, tc.want, got, tc.delta)
		})
	}

	t.Run("at the limb it is clamped, never infinite", func(t *testing.T) {
		got := airmassAt(-0.4) // refraction lifts this just above the horizon
		assert.False(t, math.IsInf(got, 0))
		assert.LessOrEqual(t, got, float64(maxAirmass))
	})
}

func TestObserve_WeatherAndSource(t *testing.T) {
	base := testTime.Truncate(time.Hour)
	q := lightpollution.SiteQuality{SQM: 20.4, Bortle: 5}

	t.Run("a freshly issued forecast reads as live", func(t *testing.T) {
		f := forecastAt(base, 3)
		f.IssuedMs = base.UnixMilli()
		s := Observe(base, testSite, Target{}, f, q)

		assert.Equal(t, SourceLive, s.Source)
		assert.Equal(t, int64(0), s.ForecastAgeMs)
		assert.Equal(t, 0.0, s.CloudPct)
		assert.Equal(t, 2.5, s.SeeingArcsec)
		assert.Equal(t, 20.4, s.SQM)
		assert.Equal(t, 5, s.Bortle)
	})

	t.Run("an older forecast reads as cached and carries its age", func(t *testing.T) {
		f := forecastAt(base, 3)
		f.IssuedMs = base.Add(-20 * time.Minute).UnixMilli()
		s := Observe(base, testSite, Target{}, f, q)

		assert.Equal(t, SourceCached, s.Source)
		assert.Equal(t, (20 * time.Minute).Milliseconds(), s.ForecastAgeMs)
	})

	t.Run("no hours at all reads as unavailable, with weather zeroed", func(t *testing.T) {
		s := Observe(base, testSite, Target{}, weather.SiteForecast{}, q)

		assert.Equal(t, SourceUnavailable, s.Source)
		assert.Zero(t, s.CloudPct)
		assert.Zero(t, s.Verdict)
		// The ephemeris half must survive a total feed outage.
		assert.NotZero(t, s.MoonPhaseAngleDeg)
	})

	t.Run("Kp rides along when the space-weather feed answered", func(t *testing.T) {
		f := forecastAt(base, 3)
		f.Kp = &weather.KpInfo{Now: 3, Max: 5, Aurora: "possible"}
		s := Observe(base, testSite, Target{}, f, q)

		assert.Equal(t, 5.0, s.KpMax)
		assert.Equal(t, "possible", s.Aurora)
	})
}

func TestObserve_Geometry(t *testing.T) {
	f := forecastAt(testTime.Truncate(time.Hour), 3)
	q := lightpollution.SiteQuality{}

	t.Run("no target leaves the target columns empty", func(t *testing.T) {
		s := Observe(testTime, testSite, Target{}, f, q)

		assert.False(t, s.TargetValid)
		assert.Zero(t, s.TargetAltDeg)
		assert.Zero(t, s.TargetAirmass)
		// Separation must stay zero rather than being measured against a target that does not exist.
		assert.Zero(t, s.MoonSepDeg)
	})

	t.Run("a target on the Moon has zero separation", func(t *testing.T) {
		ra, dec := astro.MoonPosition(testTime)
		s := Observe(testTime, testSite, Target{RADeg: ra, DecDeg: dec, Valid: true}, f, q)

		assert.True(t, s.TargetValid)
		assert.InDelta(t, 0, s.MoonSepDeg, 1e-6)
		assert.InDelta(t, s.MoonAltDeg, s.TargetAltDeg, 1e-6)
	})

	t.Run("a target opposite the Moon is far from it", func(t *testing.T) {
		ra, dec := astro.MoonPosition(testTime)
		s := Observe(testTime, testSite, Target{RADeg: math.Mod(ra+180, 360), DecDeg: -dec, Valid: true}, f, q)

		assert.Greater(t, s.MoonSepDeg, 90.0)
		assert.LessOrEqual(t, s.MoonSepDeg, 180.0)
	})

	t.Run("a target at the local zenith has airmass one", func(t *testing.T) {
		// The zenith's declination is the observer's latitude, and its RA is the local sidereal time —
		// which is exactly the RA whose hour angle is zero.
		ra := astro.HourAngleDeg(0, testSite.Lon, testTime) // hour angle of RA=0 is the LST
		s := Observe(testTime, testSite, Target{RADeg: ra, DecDeg: testSite.Lat, Valid: true}, f, q)

		assert.InDelta(t, 90, s.TargetAltDeg, 0.1)
		assert.InDelta(t, 1.0, s.TargetAirmass, 0.01)
	})
}

func TestSummarize(t *testing.T) {
	base := testTime

	sample := func(i int, src string, mut func(*Sample)) Sample {
		s := Sample{
			AtMs: base.Add(time.Duration(i) * time.Hour).UnixMilli(), Source: src,
			CloudPct: 20, SeeingArcsec: 2, Transparency: 0.7, HumidityPct: 60,
			TempC: 10, DewSpreadC: 4, WindKmh: 8, Verdict: 70,
			MoonIllum: 0.3, MoonAltDeg: -10, MoonPhaseAngleDeg: 45,
			DewRisk: "low",
		}
		if mut != nil {
			mut(&s)
		}
		return s
	}

	t.Run("an empty session summarizes to nothing, not a panic", func(t *testing.T) {
		got := Summarize(nil)
		assert.Equal(t, 0, got.Samples)
		assert.Zero(t, got.Cloud.N)
		require.NotNil(t, got.SourceCounts)
	})

	t.Run("spread and span over a normal run", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.CloudPct = 10 }),
			sample(1, SourceCached, func(s *Sample) { s.CloudPct = 30 }),
			sample(2, SourceCached, func(s *Sample) { s.CloudPct = 80 }),
		})

		assert.Equal(t, 3, got.Samples)
		assert.Equal(t, base.UnixMilli(), got.FirstMs)
		assert.Equal(t, base.Add(2*time.Hour).UnixMilli(), got.LastMs)
		assert.Equal(t, Stat{Min: 10, Median: 30, Max: 80, N: 3}, got.Cloud)
		assert.Equal(t, map[string]int{SourceLive: 1, SourceCached: 2}, got.SourceCounts)
		assert.Equal(t, 45.0, got.MoonPhaseAngleDeg)
	})

	t.Run("an even count medians across the middle pair", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.CloudPct = 10 }),
			sample(1, SourceLive, func(s *Sample) { s.CloudPct = 30 }),
		})
		assert.Equal(t, 20.0, got.Cloud.Median)
	})

	t.Run("a freezing, perfectly clear night is recorded, not discarded as unknown", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.TempC, s.CloudPct, s.WindKmh = 0, 0, 0 }),
			sample(1, SourceLive, func(s *Sample) { s.TempC, s.CloudPct, s.WindKmh = 0, 0, 0 }),
		})

		assert.Equal(t, 2, got.Temp.N, "0 °C is a real reading")
		assert.Equal(t, 2, got.Cloud.N, "0 % cloud is a perfect night")
		assert.Equal(t, 2, got.Wind.N)
	})

	t.Run("seeing, transparency and AOD treat zero as the feed being absent", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.SeeingArcsec, s.Transparency, s.AOD = 0, 0, 0 }),
			sample(1, SourceLive, func(s *Sample) { s.SeeingArcsec, s.Transparency, s.AOD = 3, 0.9, 0.2 }),
		})

		assert.Equal(t, 1, got.Seeing.N)
		assert.Equal(t, 3.0, got.Seeing.Median)
		assert.Equal(t, 1, got.Transparency.N)
		assert.Equal(t, 1, got.AOD.N)
	})

	t.Run("an all-unavailable night keeps its ephemeris but has no weather", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceUnavailable, func(s *Sample) { s.CloudPct, s.SeeingArcsec, s.TempC = 0, 0, 0 }),
			sample(1, SourceUnavailable, func(s *Sample) { s.CloudPct, s.SeeingArcsec, s.TempC = 0, 0, 0 }),
		})

		assert.Zero(t, got.Cloud.N)
		assert.Zero(t, got.Temp.N)
		assert.Equal(t, 0.3, got.MoonIllumMax)
		assert.Equal(t, map[string]int{SourceUnavailable: 2}, got.SourceCounts)
	})

	t.Run("the moon maxima catch a moonrise the medians would hide", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.MoonAltDeg, s.MoonIllum = -30, 0.6 }),
			sample(1, SourceLive, func(s *Sample) { s.MoonAltDeg, s.MoonIllum = -5, 0.6 }),
			sample(2, SourceLive, func(s *Sample) { s.MoonAltDeg, s.MoonIllum = 12, 0.61 }),
		})

		assert.Equal(t, 12.0, got.MoonAltMaxDeg)
		assert.True(t, got.MoonUp)
		assert.InDelta(t, 0.61, got.MoonIllumMax, 1e-9)
	})

	t.Run("target extremes ignore the below-horizon airmass sentinel", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) {
				s.TargetValid, s.TargetAltDeg, s.TargetAirmass, s.MoonSepDeg = true, -3, 0, 40
			}),
			sample(1, SourceLive, func(s *Sample) {
				s.TargetValid, s.TargetAltDeg, s.TargetAirmass, s.MoonSepDeg = true, 62, 1.13, 35
			}),
		})

		assert.True(t, got.TargetValid)
		assert.Equal(t, -3.0, got.TargetAltMinDeg)
		assert.Equal(t, 62.0, got.TargetAltMaxDeg)
		assert.InDelta(t, 1.13, got.TargetAirmassMin, 1e-9)
		assert.Equal(t, 35.0, got.MoonSepMinDeg)
	})

	t.Run("the worst dew risk and the strongest aurora win", func(t *testing.T) {
		got := Summarize([]Sample{
			sample(0, SourceLive, func(s *Sample) { s.DewRisk, s.Aurora, s.KpMax = "low", "unlikely", 2 }),
			sample(1, SourceLive, func(s *Sample) { s.DewRisk, s.Aurora, s.KpMax = "high", "likely", 6 }),
			sample(2, SourceLive, func(s *Sample) { s.DewRisk, s.Aurora, s.KpMax = "moderate", "possible", 4 }),
		})

		assert.Equal(t, "high", got.DewRiskWorst)
		assert.Equal(t, "likely", got.AuroraMax)
		assert.Equal(t, 6.0, got.KpMax)
	})

	t.Run("summarizing does not reorder the caller's samples", func(t *testing.T) {
		in := []Sample{
			sample(0, SourceLive, func(s *Sample) { s.CloudPct = 90 }),
			sample(1, SourceLive, func(s *Sample) { s.CloudPct = 10 }),
		}
		Summarize(in)
		assert.Equal(t, 90.0, in[0].CloudPct, "chronological order must survive")
	})
}
