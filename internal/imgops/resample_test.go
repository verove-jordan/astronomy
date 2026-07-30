package imgops

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCubicWeights_PartitionOfUnity(t *testing.T) {
	for _, tt := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.999} {
		w := CubicWeights(tt)
		sum := w[0] + w[1] + w[2] + w[3]
		assert.InDelta(t, 1.0, sum, 1e-12, "t=%v", tt)
	}
}

func TestSampleCubic_ExactAtIntegerCoords(t *testing.T) {
	const w, h = 5, 4
	src := make([]float32, w*h)
	for i := range src {
		src[i] = float32(i) * 0.31
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			got := SampleCubic(src, w, h, float64(x), float64(y))
			assert.InDelta(t, float64(src[y*w+x]), float64(got), 1e-6, "(%d,%d)", x, y)
		}
	}
}

func TestSampleCubic_ConstantPlaneStaysConstant(t *testing.T) {
	const w, h = 6, 6
	src := make([]float32, w*h)
	for i := range src {
		src[i] = 0.42
	}
	for _, p := range [][2]float64{{1.3, 2.7}, {0.5, 0.5}, {-0.4, 3.2}, {5.8, 5.9}} {
		got := SampleCubic(src, w, h, p[0], p[1])
		require.InDelta(t, 0.42, float64(got), 1e-6, "at %v (incl. edge-clamped overhang)", p)
	}
}
