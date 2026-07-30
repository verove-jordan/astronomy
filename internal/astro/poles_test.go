package astro

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPrecessFromJ2000_Polaris(t *testing.T) {
	y2026 := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)
	ra, dec := PrecessFromJ2000(PolarisRAJ2000, PolarisDecJ2000, y2026)
	t.Logf("Polaris JNow 2026: RA=%.4f Dec=%.4f", ra, dec)

	// Polaris is still approaching the pole in 2026 (closest ~2100), so its declination has risen above
	// the J2000 value toward ~+89.35°, and precession has swung its RA well past the J2000 ~38°.
	assert.Greater(t, dec, PolarisDecJ2000)
	assert.InDelta(t, 89.35, dec, 0.06)
	assert.InDelta(t, 45.0, ra, 6.0)

	// Declination keeps increasing toward the ~2100 closest approach.
	_, dec2100 := PrecessFromJ2000(PolarisRAJ2000, PolarisDecJ2000,
		time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC))
	assert.Greater(t, dec2100, dec)
}

func TestPoleStar_Hemispheres(t *testing.T) {
	when := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)

	_, decN, nameN := PoleStar(true, when)
	assert.Equal(t, "Polaris", nameN)
	assert.InDelta(t, 90, decN, 1.0) // within ~1° of the north celestial pole

	_, decS, nameS := PoleStar(false, when)
	assert.Equal(t, "σ Octantis", nameS)
	assert.InDelta(t, -90, decS, 1.5)
}

// The mount speaks the equinox of date; everything else here is J2000. A round trip through both
// conversions must land back where it started, or every GoTo inherits the error.
func TestPrecessRoundTrip(t *testing.T) {
	when := time.Date(2026, 7, 27, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		raDeg, decDeg float64
	}{
		{"M31", 10.6847, 41.2687},
		{"NGC 7000", 314.75, 44.31},
		{"near the pole", 37.95, 89.26},
		{"southern", 100.0, -60.0},
		{"across 0h", 359.9, 5.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jnowRA, jnowDec := PrecessFromJ2000(tt.raDeg, tt.decDeg, when)
			backRA, backDec := PrecessToJ2000(jnowRA, jnowDec, when)
			assert.InDelta(t, tt.decDeg, backDec, 1e-6)
			assert.InDelta(t, 0, norm180Diff(tt.raDeg, backRA), 1e-6)
		})
	}
}

// The correction must be real, not a no-op: by 2026 precession has moved coordinates by roughly a
// third of a degree — many times the field of view of a single pixel.
func TestPrecessFromJ2000_IsSignificantIn2026(t *testing.T) {
	when := time.Date(2026, 7, 27, 22, 0, 0, 0, time.UTC)
	raJ, decJ := 10.6847, 41.2687
	raN, decN := PrecessFromJ2000(raJ, decJ, when)
	sep := AngularSeparation(raJ, decJ, raN, decN)
	assert.Greater(t, sep, 0.2, "26 years of precession is about a third of a degree")
	assert.Less(t, sep, 0.5)
}

func norm180Diff(a, b float64) float64 {
	d := math.Mod(a-b+540, 360) - 180
	return d
}
