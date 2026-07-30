package grade

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rotationAbout builds the homography of a rotation by deg degrees about (cx, cy), translated by
// (tx, ty) — the shape of a real cross-night registration transform.
func rotationAbout(deg, cx, cy, tx, ty float64) [9]float64 {
	rad := deg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	return [9]float64{
		cos, -sin, cx - cos*cx + sin*cy + tx,
		sin, cos, cy - sin*cx - cos*cy + ty,
		0, 0, 1,
	}
}

var identityH = [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}

func TestRelativeH(t *testing.T) {
	t.Run("identity reference returns the frame transform", func(t *testing.T) {
		h := rotationAbout(30, 100, 80, 5, -3)
		rel, ok := RelativeH(identityH, h)
		require.True(t, ok)
		assert.InDeltaSlice(t, h[:], rel[:], 1e-9)
	})
	t.Run("frame relative to itself is identity", func(t *testing.T) {
		h := rotationAbout(140, 320, 240, 40, 25)
		rel, ok := RelativeH(h, h)
		require.True(t, ok)
		assert.InDeltaSlice(t, identityH[:], rel[:], 1e-9)
	})
	t.Run("singular reference is refused", func(t *testing.T) {
		_, ok := RelativeH([9]float64{}, identityH)
		assert.False(t, ok)
	})
}

func TestFootprintOverlap(t *testing.T) {
	const w, h = 640.0, 480.0
	cases := []struct {
		name string
		rel  [9]float64
		min  float64
		max  float64
	}{
		{"identity covers everything", identityH, 0.999, 1.0},
		{"small dither barely moves it", rotationAbout(0, 0, 0, 8, -5), 0.95, 1.0},
		// A 140° night-to-night rotation about the field centre keeps a big central overlap —
		// exactly the task #312 geometry that must NOT be treated as absurd.
		{"140° rotation about centre stays large", rotationAbout(140, w/2, h/2, 0, 0), 0.5, 1.0},
		// A false star match lands the footprint thousands of pixels away → ~zero overlap.
		{"wild translation is ~zero", rotationAbout(0, 0, 0, 6000, -4000), 0, 0.001},
		{"half-frame shift is partial", rotationAbout(0, 0, 0, w/2, 0), 0.45, 0.55},
		{"unregistered zero matrix is zero", [9]float64{}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FootprintOverlap(tc.rel, w, h)
			assert.GreaterOrEqual(t, got, tc.min)
			assert.LessOrEqual(t, got, tc.max)
		})
	}
}

func TestRotationDeg(t *testing.T) {
	for _, deg := range []float64{0, 30, 140, -35, 179} {
		got := RotationDeg(rotationAbout(deg, 100, 100, 12, -7))
		assert.InDelta(t, deg, got, 1e-9, "rotation %v", deg)
	}
}

func TestRelativeH_ReReferencesAcrossFrames(t *testing.T) {
	// Two frames registered to some Siril-chosen base: re-referencing frame B onto frame A must
	// recover exactly the A→B relative rotation, whatever the base was.
	base := rotationAbout(17, 50, 60, 3, 4)
	hA := mulH(base, rotationAbout(10, 320, 240, 0, 0))
	hB := mulH(base, rotationAbout(50, 320, 240, 0, 0))
	rel, ok := RelativeH(hA, hB)
	require.True(t, ok)
	assert.InDelta(t, 40, RotationDeg(rel), 1e-6)
}
