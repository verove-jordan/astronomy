package astro

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJulianDate_J2000(t *testing.T) {
	jd := JulianDate(time.Date(2000, time.January, 1, 12, 0, 0, 0, time.UTC))
	assert.InDelta(t, 2451545.0, jd, 1e-6)
}

func TestGMST_Meeus12a(t *testing.T) {
	// Meeus, Astronomical Algorithms, example 12.a: 1987 April 10, 0h UT → mean sidereal 197.693195°.
	g := GMST(time.Date(1987, time.April, 10, 0, 0, 0, 0, time.UTC))
	assert.InDelta(t, 197.693195, g, 0.01)
}

func TestHorizontal_Meeus13b(t *testing.T) {
	// Meeus example 13.b: 1987 April 10 19:21:00 UT, Washington (lat +38.921389°, long 77.065556° W),
	// Venus α=347.3193°, δ=−6.71992° → altitude 15.1249°, azimuth (from N) 248.0337°.
	when := time.Date(1987, time.April, 10, 19, 21, 0, 0, time.UTC)
	alt, az := Horizontal(347.3193, -6.71992, 38.921389, -77.065556, when)
	assert.InDelta(t, 15.1249, alt, 0.05)
	assert.InDelta(t, 248.0337, az, 0.1)
}

func TestAngularSeparation_Meeus17a(t *testing.T) {
	// Meeus example 17.a: Arcturus and Spica are 32.7930° apart.
	d := AngularSeparation(213.9154, 19.1825, 201.2983, -11.1614)
	assert.InDelta(t, 32.7930, d, 0.01)
}

func TestSunPosition_1992Oct13(t *testing.T) {
	// Meeus example 25.a: 1992 October 13, 0h UT → RA ≈ 198.38°, Dec ≈ −7.785°.
	ra, dec := SunPosition(time.Date(1992, time.October, 13, 0, 0, 0, 0, time.UTC))
	assert.InDelta(t, 198.38, ra, 0.1)
	assert.InDelta(t, -7.785, dec, 0.1)
}

func TestMoonIllumination_1992Apr12(t *testing.T) {
	// Meeus example 48.a: 1992 April 12, 0h UT → illuminated fraction 0.6786.
	k := MoonIllumination(time.Date(1992, time.April, 12, 0, 0, 0, 0, time.UTC))
	assert.InDelta(t, 0.6786, k, 0.05)
}

func TestAirmass_KastenYoung(t *testing.T) {
	tests := []struct {
		name string
		alt  float64
		want float64
		tol  float64
	}{
		{"zenith", 90, 1.000, 0.002},
		{"thirty", 30, 1.995, 0.01},
		{"horizon", 0, 37.9, 0.6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, Airmass(tt.alt), tt.tol)
		})
	}
	assert.True(t, math.IsInf(Airmass(-5), 1), "below horizon must be +Inf")
	// Strictly decreasing with altitude.
	assert.Greater(t, Airmass(10), Airmass(30))
	assert.Greater(t, Airmass(30), Airmass(60))
	assert.Greater(t, Airmass(60), Airmass(90))
}

func TestTransitAltitude(t *testing.T) {
	// M101 (dec +54.349°) from Paris (lat +48.857°): 90 − |48.857 − 54.349| = 84.508°.
	assert.InDelta(t, 84.508, TransitAltitude(48.857, 54.349), 1e-3)
}

func TestHourAngleForAltitude(t *testing.T) {
	tests := []struct {
		name       string
		alt        float64
		lat, dec   float64
		wantStatus AltCrossing
		wantHA     float64 // checked only when Crosses
	}{
		{"never rises", 30, 48, -60, AlwaysBelow, 0},
		{"circumpolar above min", 30, 48, 80, AlwaysAbove, 0},
		{"normal crossing", 30, 48, 20, Crosses, 66.97},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ha, status := HourAngleForAltitude(tt.alt, tt.lat, tt.dec)
			assert.Equal(t, tt.wantStatus, status)
			if tt.wantStatus == Crosses {
				assert.InDelta(t, tt.wantHA, ha, 0.1)
			}
		})
	}
}

func TestNightWindow(t *testing.T) {
	t.Run("mid-latitude winter has astronomical darkness", func(t *testing.T) {
		w := NightWindow(time.Date(2026, time.January, 15, 22, 0, 0, 0, time.UTC), 45, 0, -18)
		assert.True(t, w.End.After(w.Start))
		assert.Greater(t, w.Hours(), 0.0)
		assert.False(t, w.NoAstroDark)
		assert.Equal(t, "astronomical", w.Kind)
	})
	t.Run("high-latitude midsummer has no astronomical darkness", func(t *testing.T) {
		w := NightWindow(time.Date(2026, time.June, 21, 12, 0, 0, 0, time.UTC), 65, 0, -18)
		assert.True(t, w.End.After(w.Start))
		assert.True(t, w.NoAstroDark)
		assert.NotEqual(t, "astronomical", w.Kind)
	})
}

func TestHoursAboveAltInWindow_Bounds(t *testing.T) {
	w := DarkWindow{
		Start: time.Date(2026, time.January, 15, 18, 0, 0, 0, time.UTC),
		End:   time.Date(2026, time.January, 16, 0, 0, 0, 0, time.UTC),
	}
	// Near-pole object from a mid-northern site is above 30° the whole night.
	above := HoursAboveAltInWindow(0, 89, 30, 48, 0, w)
	assert.InDelta(t, w.Hours(), above, 1e-6)
	// Far-southern object never reaches 30°.
	never := HoursAboveAltInWindow(0, -80, 30, 48, 0, w)
	assert.Equal(t, 0.0, never)
}

func TestAltitudeCrossings_SunSetRise(t *testing.T) {
	lat, lon := 48.8566, 2.3522 // Paris
	mid := SolarMidnight(time.Date(2026, time.January, 15, 23, 0, 0, 0, time.UTC), lat, lon)
	sunAlt := func(tt time.Time) float64 {
		ra, dec := SunPosition(tt)
		a, _ := Horizontal(ra, dec, lat, lon, tt)
		return a
	}
	crossings := AltitudeCrossings(sunAlt, mid.Add(-12*time.Hour), mid.Add(12*time.Hour), 5*time.Minute, -0.833)

	var set, rise time.Time
	var hasSet, hasRise bool
	for _, c := range crossings {
		if !c.Rising && !c.Time.After(mid) {
			set, hasSet = c.Time, true // latest set before midnight
		}
		if c.Rising && c.Time.After(mid) && !hasRise {
			rise, hasRise = c.Time, true // first rise after midnight
		}
	}
	assert.True(t, hasSet, "expected a sunset before solar midnight")
	assert.True(t, hasRise, "expected a sunrise after solar midnight")
	assert.True(t, set.Before(rise), "sunset must precede sunrise")
}
