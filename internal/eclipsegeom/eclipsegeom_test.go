package eclipsegeom

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// piriac is where the 12 Aug 2026 clips were shot, read from their own
// com.apple.quicktime.location.ISO6709 tag.
var piriac = Site{LatDeg: 47.2783, LonDeg: -2.4948, ElevM: 20}

func utc(y int, mo time.Month, d, h, mi int, sec ...int) time.Time {
	s := 0
	if len(sec) > 0 {
		s = sec[0]
	}
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

func TestMaximum_12Aug2026_Piriac(t *testing.T) {
	at, c := Maximum(utc(2026, 8, 12, 16, 0), utc(2026, 8, 12, 20, 0), piriac)

	t.Logf("max %s obsc=%.4f sep=%.1f\" Rs=%.1f\" Rm=%.1f\" PA=%.1f alt=%.1f q=%.1f flat=%.4f",
		at.Format(time.RFC3339), c.Obscuration, c.SepArcsec, c.SunRadiusArcsec,
		c.MoonRadiusArcsec, c.MoonPADeg, c.SunAltDeg, c.ParallacticDeg, c.RefractFlatten)

	assert.WithinDuration(t, utc(2026, 8, 12, 18, 21), at, 90*time.Second)
	assert.InDelta(t, 0.9627, c.Obscuration, 0.005, "deep partial, just short of the totality path")
	// The Moon was the larger disc — this eclipse was total in northern Spain.
	assert.Greater(t, c.MoonRadiusArcsec, c.SunRadiusArcsec)
	assert.InDelta(t, 9.6, c.SunAltDeg, 1.0)
}

func TestContacts_12Aug2026_EndedBeforeSunset(t *testing.T) {
	at, _ := Maximum(utc(2026, 8, 12, 16, 0), utc(2026, 8, 12, 20, 0), piriac)
	first, last, ok := Contacts(at, piriac)
	require.True(t, ok)

	t.Logf("C1 %s  C4 %s", first.Format(time.RFC3339), last.Format(time.RFC3339))
	assert.WithinDuration(t, utc(2026, 8, 12, 17, 24, 37), first, 90*time.Second)
	assert.WithinDuration(t, utc(2026, 8, 12, 19, 13, 44), last, 90*time.Second)
	// The session's last clip runs 19:12:33 -> 19:13:58Z, so it straddles last contact: the Moon
	// leaves the Sun fourteen seconds before the recording stops.
	assert.True(t, last.Before(utc(2026, 8, 12, 19, 13, 58)), "the last clip outlasts the eclipse")
	assert.True(t, last.After(utc(2026, 8, 12, 19, 12, 33)), "the last clip starts while still eclipsed")
}

// TestAt_SessionClips checks the six clips against the phases the sequence planner will build its
// ladder from. These are the numbers the coverage table in docs/modes/eclipse.md quotes.
func TestAt_SessionClips(t *testing.T) {
	tests := []struct {
		name     string
		at       time.Time
		wantObsc float64
		wantPA   float64
	}{
		{"clip 1, just after first contact", utc(2026, 8, 12, 17, 30), 0.036, 295},
		{"clip 2, ingress", utc(2026, 8, 12, 17, 48), 0.302, 293},
		{"clip 3 start, deep ingress", utc(2026, 8, 12, 18, 12), 0.812, 280},
		{"clip 3 end, egress", utc(2026, 8, 12, 18, 28), 0.847, 139},
		{"clip 4, egress", utc(2026, 8, 12, 18, 42), 0.526, 125},
		{"clip 5, egress", utc(2026, 8, 12, 18, 50), 0.352, 123},
		{"clip 6, all but over", utc(2026, 8, 12, 19, 13), 0.002, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := At(tt.at, piriac)
			t.Logf("obsc=%.3f PA=%.1f alt=%.2f flat=%.4f", c.Obscuration, c.MoonPADeg, c.SunAltDeg, c.RefractFlatten)
			assert.InDelta(t, tt.wantObsc, c.Obscuration, 0.01)
			assert.InDelta(t, tt.wantPA, c.MoonPADeg, 2)
		})
	}
}

// TestMaximum_Totality2017 is the external check: Madras, Oregon sat on the centre line of the
// 21 Aug 2017 eclipse, so the obscuration there must reach exactly 1.
func TestMaximum_Totality2017(t *testing.T) {
	madras := Site{LatDeg: 44.6333, LonDeg: -121.1333, ElevM: 686}
	at, c := Maximum(utc(2017, 8, 21, 16, 0), utc(2017, 8, 21, 19, 0), madras)

	t.Logf("Madras max %s obsc=%.5f sep=%.1f\"", at.Format(time.RFC3339), c.Obscuration, c.SepArcsec)
	assert.Equal(t, 1.0, c.Obscuration, "Madras was on the centre line")
	assert.WithinDuration(t, utc(2017, 8, 21, 17, 20, 30), at, 2*time.Minute)
}

// TestMaximum_OutsideThePath2017 is the other half of the same check: Seattle was well north of the
// path and saw a deep partial, never totality.
func TestMaximum_OutsideThePath2017(t *testing.T) {
	seattle := Site{LatDeg: 47.6062, LonDeg: -122.3321, ElevM: 50}
	_, c := Maximum(utc(2017, 8, 21, 16, 0), utc(2017, 8, 21, 19, 0), seattle)

	t.Logf("Seattle obsc=%.4f", c.Obscuration)
	assert.Less(t, c.Obscuration, 1.0)
	assert.InDelta(t, 0.92, c.Obscuration, 0.04)
}

func TestObscuration_Geometry(t *testing.T) {
	tests := []struct {
		name            string
		sep, rSun, rMon float64
		want            float64
	}{
		{"discs apart", 2000, 960, 970, 0},
		{"discs exactly tangent", 1930, 960, 970, 0},
		{"moon larger and concentric is total", 0, 960, 970, 1},
		{"moon smaller and concentric is annular", 0, 960, 940, (940.0 / 960) * (940.0 / 960)},
		{"half the diameter covered is less than half the area", 960, 960, 960, 0.391},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, obscuration(tt.sep, tt.rSun, tt.rMon), 0.01)
		})
	}
}

func TestRefractFlatten_OnlyBitesNearTheHorizon(t *testing.T) {
	const solarRadiusDeg = 0.2665
	high := refractFlatten(45, solarRadiusDeg)
	mid := refractFlatten(9.6, solarRadiusDeg)
	low := refractFlatten(1.3, solarRadiusDeg)

	t.Logf("45deg=%.4f  9.6deg=%.4f  1.3deg=%.4f", high, mid, low)
	assert.Equal(t, 1.0, high, "reported as exactly round well above the horizon")
	assert.Less(t, low, 0.97, "the last clip's Sun is visibly oval")
	assert.Less(t, low, mid, "flattening grows as the Sun sets")
}

func TestTimeAtObscuration_RoundTripsOnBothSides(t *testing.T) {
	max, peak := Maximum(utc(2026, 8, 12, 16, 0), utc(2026, 8, 12, 20, 0), piriac)
	require.Greater(t, peak.Obscuration, 0.9)

	for _, f := range []float64{0.02, 0.25, 0.50, 0.75, 0.90} {
		for _, ingress := range []bool{true, false} {
			at, ok := TimeAtObscuration(f, ingress, piriac, max)
			require.True(t, ok, "f=%.2f ingress=%v", f, ingress)
			assert.InDelta(t, f, At(at, piriac).Obscuration, 0.005, "f=%.2f ingress=%v", f, ingress)
			if ingress {
				assert.True(t, at.Before(max), "ingress instants precede maximum")
				continue
			}
			assert.True(t, at.After(max), "egress instants follow maximum")
		}
	}
}

func TestTimeAtObscuration_RefusesAPhaseTheEclipseNeverReached(t *testing.T) {
	max, _ := Maximum(utc(2026, 8, 12, 16, 0), utc(2026, 8, 12, 20, 0), piriac)
	_, ok := TimeAtObscuration(0.999, true, piriac, max)
	assert.False(t, ok, "this eclipse never went total here")
}
