package astro

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestPlanetElongationBounds checks the defining geometry of the inferior planets: Mercury never
// strays more than ~28° from the Sun and Venus never more than ~47°. Sampled across a full year, this
// catches gross errors in the heliocentric→geocentric assembly without needing an external ephemeris.
func TestPlanetElongationBounds(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var maxMerc, maxVen float64
	for day := 0; day < 365; day++ {
		at := start.AddDate(0, 0, day)
		if e := PlanetPosition(Mercury, at).ElongationDeg; e > maxMerc {
			maxMerc = e
		}
		if e := PlanetPosition(Venus, at).ElongationDeg; e > maxVen {
			maxVen = e
		}
	}
	assert.Greater(t, maxMerc, 17.0, "Mercury should reach a sizeable elongation during the year")
	assert.Less(t, maxMerc, 28.5, "Mercury elongation must stay within its physical maximum")
	assert.Greater(t, maxVen, 40.0, "Venus should reach a wide elongation during the year")
	assert.Less(t, maxVen, 47.5, "Venus elongation must stay within its physical maximum")
}

// TestMarsOpposition2025 anchors the position to a known event: Mars reached opposition on
// 2025-01-16 in Gemini (RA ≈ 7h, Dec ≈ +25°), so its elongation must be near 180°.
func TestMarsOpposition2025(t *testing.T) {
	at := time.Date(2025, 1, 16, 2, 0, 0, 0, time.UTC)
	st := PlanetPosition(Mars, at)
	assert.Greater(t, st.ElongationDeg, 168.0, "Mars near opposition should be nearly opposite the Sun")
	// At opposition the planet sits near the anti-solar point: the Sun was at RA ≈ 298°, Dec ≈ −21°,
	// so Mars must be near RA ≈ 118°, Dec ≈ +25° (Gemini).
	assert.InDelta(t, 118.0, st.RADeg, 9.0, "Mars RA near opposition (Gemini)")
	assert.InDelta(t, 24.0, st.DecDeg, 9.0, "Mars Dec near opposition")
	assert.Less(t, st.Magnitude, -1.0, "Mars at opposition is bright")
}

// TestPlanetSanity verifies every planet returns coordinates and magnitudes in sane ranges.
func TestPlanetSanity(t *testing.T) {
	at := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	for _, p := range Planets {
		st := PlanetPosition(p, at)
		assert.GreaterOrEqual(t, st.RADeg, 0.0)
		assert.Less(t, st.RADeg, 360.0)
		assert.GreaterOrEqual(t, st.DecDeg, -90.0)
		assert.LessOrEqual(t, st.DecDeg, 90.0)
		assert.Greater(t, st.GeoDistAU, 0.0)
		assert.Greater(t, st.Magnitude, -6.0)
		assert.Less(t, st.Magnitude, 10.0)
	}
}
