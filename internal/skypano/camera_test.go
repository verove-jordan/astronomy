package skypano

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCamera_ProjectUnprojectRoundTrip(t *testing.T) {
	// A 24 mm-equivalent phone frame: 72 degrees across the long axis, where a tangent-plane model
	// would already be hopeless and a pinhole is still exact.
	cam := Camera{
		R:  SetRotation([3]float64{1, 0, 0}, [3]float64{0, 1, 0}, [3]float64{0, 0, 1}),
		F:  FocalPixels(24, 4032, 3024),
		Cx: 2016, Cy: 1512,
	}

	for _, tt := range []struct {
		name string
		x, y float64
	}{
		{"centre", 2016, 1512},
		{"long-axis edge", 4031, 1512},
		{"short-axis edge", 2016, 3023},
		{"corner, 42 degrees off axis", 4031, 3023},
		{"opposite corner", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := cam.Unproject(tt.x, tt.y)
			assert.InDelta(t, 1.0, math.Sqrt(dot3(v, v)), 1e-12, "unprojection must be a unit vector")

			x, y, ok := cam.Project(v)
			require.True(t, ok)
			assert.InDelta(t, tt.x, x, 1e-9)
			assert.InDelta(t, tt.y, y, 1e-9)
		})
	}
}

func TestCamera_RoundTripWithDistortion(t *testing.T) {
	cam := Camera{
		R: SetRotation([3]float64{1, 0, 0}, [3]float64{0, 1, 0}, [3]float64{0, 0, 1}),
		F: 2796, K1: -0.08,
		Cx: 2016, Cy: 1512,
	}

	for _, p := range [][2]float64{{2016, 1512}, {3500, 2500}, {200, 300}} {
		v := cam.Unproject(p[0], p[1])
		x, y, ok := cam.Project(v)

		require.True(t, ok)
		// The inverse is a fixed-point iteration, so it converges to a few parts in a million of a
		// pixel rather than exactly — orders of magnitude below any centroid precision that matters.
		assert.InDelta(t, p[0], x, 1e-4, "the iterative distortion inverse must round-trip")
		assert.InDelta(t, p[1], y, 1e-4)
	}
}

func TestCamera_RejectsDirectionsBehind(t *testing.T) {
	cam := Camera{
		R: SetRotation([3]float64{1, 0, 0}, [3]float64{0, 1, 0}, [3]float64{0, 0, 1}),
		F: 2796, Cx: 2016, Cy: 1512,
	}

	_, _, ok := cam.Project([3]float64{0, 0, -1})

	assert.False(t, ok, "a direction behind the camera has no image, and must not fold onto one")
}

func TestFocalPixels(t *testing.T) {
	// 24 mm equivalent on a 4:3 frame spans 71.6 degrees along its long edge.
	f := FocalPixels(24, 4032, 3024)

	half := math.Atan(2016/f) * 180 / math.Pi

	assert.InDelta(t, 35.8, half, 0.1)
}

func TestRADecVecRoundTrip(t *testing.T) {
	for _, tt := range []struct{ ra, dec float64 }{
		{0, 0}, {279.23, 38.78}, {310.36, 45.28}, {37.95, 89.26}, {180, -60},
	} {
		ra, dec := VecToRADec(RADecToVec(tt.ra, tt.dec))

		assert.InDelta(t, tt.ra, ra, 1e-9)
		assert.InDelta(t, tt.dec, dec, 1e-9)
	}
}
