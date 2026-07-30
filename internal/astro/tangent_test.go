package astro

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTangentPlane_RoundTrip(t *testing.T) {
	centers := []struct {
		name      string
		ra0, dec0 float64
	}{
		{"equator", 0, 0},
		{"mid declination", 180, 45},
		{"southern", 210.4, -33.2},
		{"ra wrap", 359.8, -30},
		{"near pole", 10, 89.9},
	}
	offsets := [][2]float64{{0, 0}, {0.7, 0}, {0, -0.7}, {-0.45, 0.6}, {1.2, 1.2}}
	for _, c := range centers {
		t.Run(c.name, func(t *testing.T) {
			for _, off := range offsets {
				ra, dec := TangentSky(c.ra0, c.dec0, off[0], off[1])
				xi, eta, ok := TangentPlane(c.ra0, c.dec0, ra, dec)
				require.True(t, ok)
				assert.InDelta(t, off[0], xi, 1e-9)
				assert.InDelta(t, off[1], eta, 1e-9)
			}
		})
	}
}

func TestTangentPlane_KnownOffsets(t *testing.T) {
	// 1° east at the equator: ξ = tan(1°) in degrees ≈ 1.0001, η = 0. East is +ξ.
	xi, eta, ok := TangentPlane(0, 0, 1, 0)
	require.True(t, ok)
	assert.InDelta(t, 1.0, xi, 1e-3)
	assert.InDelta(t, 0.0, eta, 1e-9)

	// 1° north at dec 60: η ≈ +1, ξ = 0. North is +η.
	xi, eta, ok = TangentPlane(120, 60, 120, 61)
	require.True(t, ok)
	assert.InDelta(t, 0.0, xi, 1e-9)
	assert.InDelta(t, 1.0, eta, 1e-3)

	// RA wrap: a point 0.4° east of a center at 359.8° sits at RA 0.2°.
	xi, eta, ok = TangentPlane(359.8, 0, 0.2, 0)
	require.True(t, ok)
	assert.InDelta(t, 0.4, xi, 1e-3)
	assert.InDelta(t, 0.0, eta, 1e-9)
}

func TestTangentPlane_AntipodeRejected(t *testing.T) {
	_, _, ok := TangentPlane(10, 20, 190, -20)
	assert.False(t, ok)
}

func TestTangentSky_RANormalized(t *testing.T) {
	ra, _ := TangentSky(359.9, 10, 0.5, 0)
	assert.GreaterOrEqual(t, ra, 0.0)
	assert.Less(t, ra, 360.0)
}
