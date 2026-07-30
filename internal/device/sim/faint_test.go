package sim

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole point of generating from sky cells: the same patch of sky must contain the same stars
// however it is looked at. Without this, two exposures of one field would share no stars, and every
// downstream measurement — plate solving, star matching, dither feedback — would be meaningless.
func TestFaintStars_SameSkyFromDifferentPointings(t *testing.T) {
	// Two overlapping pointings, 0.2° apart.
	a := faintStars(120.0, 30.0, 0.5, defaultFaintPerDeg2)
	b := faintStars(120.2, 30.0, 0.5, defaultFaintPerDeg2)
	require.NotEmpty(t, a)
	require.NotEmpty(t, b)

	// Every star in the overlap must appear in both lists, at identical coordinates.
	inB := map[[2]float64]float64{}
	for _, s := range b {
		inB[[2]float64{s.RADeg, s.DecDeg}] = s.Mag
	}
	overlap, matched := 0, 0
	for _, s := range a {
		// Is this star inside b's field too?
		if angularSepDeg(s.RADeg, s.DecDeg, 120.2, 30.0) > 0.5 {
			continue
		}
		overlap++
		if mag, ok := inB[[2]float64{s.RADeg, s.DecDeg}]; ok {
			assert.Equal(t, s.Mag, mag, "the same star must have the same brightness")
			matched++
		}
	}
	require.Positive(t, overlap, "the two fields must actually overlap for this test to mean anything")
	assert.Equal(t, overlap, matched,
		"every star in the overlap must be identical from both pointings")
}

// Determinism across calls — the same field twice must be byte-identical.
func TestFaintStars_Deterministic(t *testing.T) {
	a := faintStars(200, -15, 0.6, defaultFaintPerDeg2)
	b := faintStars(200, -15, 0.6, defaultFaintPerDeg2)
	require.Equal(t, len(a), len(b))
	for i := range a {
		assert.Equal(t, a[i], b[i])
	}
}

// The density must be roughly what was asked for — this is what decides whether a frame is solvable.
func TestFaintStars_DensityIsAboutRight(t *testing.T) {
	const radius = 1.0
	area := math.Pi * radius * radius // square degrees, near enough at this scale
	for _, dec := range []float64{0, 45, 75, -60} {
		stars := faintStars(100, dec, radius, defaultFaintPerDeg2)
		got := float64(len(stars)) / area
		assert.InEpsilon(t, defaultFaintPerDeg2, got, 0.25,
			"density at dec %.0f° should be near the requested value", dec)
	}
}

// A real ASI1600 field on a 740 mm scope must end up with enough stars for Siril, whose plate solver
// refuses fewer than six. This is the number that was actually failing.
func TestFaintStars_EnoughForAPlateSolve(t *testing.T) {
	scale := 206.265 * 3.8 / 740                            // arcsec per pixel
	halfDiag := 0.5 * math.Hypot(4656, 3520) * scale / 3600 // degrees to the sensor corner
	stars := faintStars(350.85, 58.8, halfDiag*1.15, defaultFaintPerDeg2)
	assert.Greater(t, len(stars), 500,
		"a real field of this size holds thousands of stars; Siril needs at least six to solve")
}

// Magnitudes must span the intended range and be weighted towards the faint end, the way a real
// star field is — a uniform draw would put implausibly many bright stars in every frame.
func TestFaintStars_MagnitudeDistribution(t *testing.T) {
	stars := faintStars(100, 20, 1.0, defaultFaintPerDeg2)
	require.NotEmpty(t, stars)

	brightHalf, faintHalf := 0, 0
	mid := (faintMagMin + faintMagMax) / 2
	for _, s := range stars {
		assert.GreaterOrEqual(t, s.Mag, faintMagMin)
		assert.LessOrEqual(t, s.Mag, faintMagMax)
		if s.Mag < mid {
			brightHalf++
		} else {
			faintHalf++
		}
	}
	assert.Greater(t, faintHalf, brightHalf*3,
		"faint stars must vastly outnumber bright ones, as in a real field")
}

// Asking for none must give none — the negative-means-none convention this package uses.
func TestFaintStars_CanBeDisabled(t *testing.T) {
	assert.Empty(t, faintStars(100, 20, 1.0, -1))
	assert.Empty(t, faintStars(100, 20, 0, defaultFaintPerDeg2))
}

// Fields crossing 0h RA must not come back empty or double-counted.
func TestFaintStars_AcrossTheRAWrap(t *testing.T) {
	stars := faintStars(0.1, 10, 0.5, defaultFaintPerDeg2)
	require.NotEmpty(t, stars)
	seen := map[[2]float64]bool{}
	for _, s := range stars {
		key := [2]float64{s.RADeg, s.DecDeg}
		assert.False(t, seen[key], "a star must not be generated twice")
		seen[key] = true
		assert.GreaterOrEqual(t, s.RADeg, 0.0)
		assert.Less(t, s.RADeg, 360.0)
		assert.LessOrEqual(t, angularSepDeg(s.RADeg, s.DecDeg, 0.1, 10), 0.5+1e-9)
	}
	// The wrap must not halve the count: compare with an equivalent field away from 0h.
	away := faintStars(180.1, 10, 0.5, defaultFaintPerDeg2)
	assert.InEpsilon(t, float64(len(away)), float64(len(stars)), 0.3)
}

// Near the pole, lines of RA converge; the density must not blow up there.
func TestFaintStars_DensityHoldsNearThePole(t *testing.T) {
	const radius = 0.8
	area := math.Pi * radius * radius
	stars := faintStars(45, 88, radius, defaultFaintPerDeg2)
	got := float64(len(stars)) / area
	assert.InEpsilon(t, defaultFaintPerDeg2, got, 0.4,
		"cells are widened by 1/cos(dec) precisely so the pole is not over-populated")
}

// angularSepDeg is the great-circle separation between two sky positions.
func angularSepDeg(ra1, dec1, ra2, dec2 float64) float64 {
	const d = math.Pi / 180
	s1, c1 := math.Sincos(dec1 * d)
	s2, c2 := math.Sincos(dec2 * d)
	cos := s1*s2 + c1*c2*math.Cos((ra1-ra2)*d)
	return math.Acos(math.Min(1, math.Max(-1, cos))) / d
}
