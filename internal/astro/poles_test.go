package astro

import (
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
