package polaralign

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// when is a fixed instant; the longitude in each clock-position case is derived from it so the pole
// star's hour angle is exactly 0 or 90°, independent of the date.
var when = time.Date(2026, time.June, 29, 22, 0, 0, 0, time.UTC)

func TestCompute_ClockPosition(t *testing.T) {
	gmst := astro.GMST(when)
	raN, _, _ := astro.PoleStar(true, when)
	raS, _, _ := astro.PoleStar(false, when)

	t.Run("north HA 0 → 6 o'clock (inverting scope)", func(t *testing.T) {
		r := Compute(when, 48.0, raN-gmst) // LST == RA → HA 0
		assert.InDelta(t, 6.0, r.ClockHour, 0.02)
	})
	t.Run("north HA 6h → 3 o'clock", func(t *testing.T) {
		r := Compute(when, 48.0, raN+90-gmst) // HA 90°
		assert.InDelta(t, 3.0, r.ClockHour, 0.02)
	})
	t.Run("south flips direction (HA 6h → 9 o'clock)", func(t *testing.T) {
		r := Compute(when, -33.0, raS+90-gmst) // HA 90°
		assert.InDelta(t, 9.0, r.ClockHour, 0.02)
	})
}

func TestCompute_Separation(t *testing.T) {
	north := Compute(when, 48.0, 2.0)
	assert.InDelta(t, 0.65, north.SeparationDeg, 0.15) // Polaris ~0.65° from the NCP in 2026
	south := Compute(when, -33.0, 151.0)
	assert.InDelta(t, 1.0, south.SeparationDeg, 0.2) // σ Octantis ~1° from the SCP
}

func TestCompute_Montigny(t *testing.T) {
	// Montigny-sur-Loing 48.28 N, 2.78 E — the cross-check site (compare HA/clock against PolarScope
	// Align / Stellarium for this instant; this logs the values for that manual calibration check).
	r := Compute(when, 48.28, 2.78)
	t.Logf("Montigny: HA=%.2f° clock=%.2f sep=%.3f° alt=%.2f° az=%.2f° lst=%.2f°",
		r.HADeg, r.ClockHour, r.SeparationDeg, r.AltDeg, r.AzDeg, r.LSTDeg)
	assert.Equal(t, "north", r.Hemisphere)
	assert.Equal(t, "Polaris", r.PoleStarName)
	assert.InDelta(t, 48.28, r.AltDeg, 1.5) // pole-star altitude ≈ latitude
	assert.True(t, r.PoleStarVisible)
	assert.False(t, r.LatTooLow)
	assert.GreaterOrEqual(t, r.ClockHour, 0.0)
	assert.Less(t, r.ClockHour, 12.0)
}

func TestCompute_LatTooLow(t *testing.T) {
	r := Compute(when, 5.0, 0.0)
	assert.True(t, r.LatTooLow)
}
