package planetary

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRejectLeastSharp(t *testing.T) {
	// Scores by frame index 1..5; sharpest are 3 and 5.
	scores := []float64{0.1, 0.2, 0.9, 0.05, 0.8}

	// Keep best 40% (2 of 5) → reject the other 3 (indices 1, 2, 4), sorted.
	assert.Equal(t, []int{1, 2, 4}, rejectLeastSharp(scores, 40))

	// Keep 100% → reject none.
	assert.Empty(t, rejectLeastSharp(scores, 100))

	// Always keep at least one even at 0%.
	assert.Len(t, rejectLeastSharp(scores, 0), len(scores)-1)
}

func TestLaplacianVariance(t *testing.T) {
	const w, h = 16, 16
	flat := make([]float64, w*h)
	for i := range flat {
		flat[i] = 100
	}
	sharp := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// high-frequency checkerboard
			sharp[y*w+x] = math.Mod(float64(x+y), 2) * 200
		}
	}
	assert.InDelta(t, 0, laplacianVariance(flat, w, h), 1e-9, "a flat field has ~zero sharpness")
	assert.Greater(t, laplacianVariance(sharp, w, h), laplacianVariance(flat, w, h), "detailed frame is sharper")
}
