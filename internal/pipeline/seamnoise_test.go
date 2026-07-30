package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoZoneGrid builds a coverage grid for a w×h canvas whose left half saw shallow frames and
// right half deep ones.
func twoZoneGrid(w, h, shallow, deep int) *coverageGrid {
	cv := canvasSpec{W: w, H: h}
	g := &coverageGrid{
		W:      (w + coverageDownscale - 1) / coverageDownscale,
		H:      (h + coverageDownscale - 1) / coverageDownscale,
		Scale:  coverageDownscale,
		Canvas: cv,
	}
	g.Counts = make([]uint16, g.W*g.H)
	for gy := 0; gy < g.H; gy++ {
		for gx := 0; gx < g.W; gx++ {
			c := shallow
			if gx >= g.W/2 {
				c = deep
			}
			g.Counts[gy*g.W+gx] = uint16(c)
		}
	}
	g.Frames = deep
	return g
}

func TestSeamNoiseWeights_UniformCoverageNil(t *testing.T) {
	g := twoZoneGrid(512, 512, 5, 5)
	weights, reason := seamNoiseWeights(g, 512, 512)
	assert.Nil(t, weights)
	assert.Contains(t, reason, "nearly uniform")
}

func TestSeamNoiseWeights_TwoZoneRampIsSmoothAndCapped(t *testing.T) {
	const w, h = 512, 512
	g := twoZoneGrid(w, h, 1, 5)
	weights, reason := seamNoiseWeights(g, w, h)
	require.Empty(t, reason)
	require.Len(t, weights, w*h)

	midRow := (h / 2) * w
	deepCore := weights[midRow+w-16]
	shallowEdge := weights[midRow+16]
	assert.InDelta(t, 0, float64(deepCore), 0.05, "full-depth core gets ~no extra denoise")
	assert.Greater(t, float64(shallowEdge), 0.5, "shallow zone gets a real weight")
	assert.LessOrEqual(t, float64(shallowEdge), seamNoiseMaxWeight)

	// The ramp across the boundary must be smooth: no adjacent-pixel jump anywhere near the step.
	maxJump := 0.0
	for x := w/2 - 96; x < w/2+96; x++ {
		d := float64(weights[midRow+x]) - float64(weights[midRow+x+1])
		if d < 0 {
			d = -d
		}
		if d > maxJump {
			maxJump = d
		}
	}
	assert.Less(t, maxJump, 0.08, "coverage blur must fade the weight, not step it")
}

func TestSeamNoiseWeights_NeverCoveredZero(t *testing.T) {
	const w, h = 512, 512
	g := twoZoneGrid(w, h, 1, 5)  // shallow left, deep right…
	for gy := 0; gy < g.H; gy++ { // …and the leftmost third never covered at all
		for gx := 0; gx < g.W/3; gx++ {
			g.Counts[gy*g.W+gx] = 0
		}
	}
	weights, reason := seamNoiseWeights(g, w, h)
	require.Empty(t, reason)

	midRow := (h / 2) * w
	for x := 0; x < (g.W/3)*coverageDownscale-coverageDownscale; x++ {
		require.Zero(t, weights[midRow+x], "never-covered pixel %d must stay exactly 0", x)
	}
	// The shallow band is narrow (~85 px) vs the 96 px blur, so its weight is heavily smoothed —
	// it must still be clearly non-zero at the band's centre.
	assert.Greater(t, float64(weights[midRow+(g.W/3+g.W/2)/2*coverageDownscale]), 0.2,
		"shallow covered zone keeps its weight")
}

func TestSeamNoiseWeights_GridMismatch(t *testing.T) {
	g := twoZoneGrid(512, 512, 1, 5)
	weights, reason := seamNoiseWeights(g, 1024, 1024)
	assert.Nil(t, weights)
	assert.Contains(t, reason, "does not match")
}
