package trail

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefine_GaussianWidth(t *testing.T) {
	p := noisyPlane(300, 300, 0.05, 0.003, 10)
	addStreak(p, 300, 300, 75, 150, 225, 150, 0.05, 2.0) // horizontal, σ⊥=2 ⇒ FWHM≈4.71, 50% span

	segs := DetectSegments(p, 300, 300, DefaultParams(5))
	require.Len(t, segs, 1)
	assert.InDelta(t, 4.71, segs[0].Width, 1.0)
}

func TestRefine_ExtentToBorders(t *testing.T) {
	p := noisyPlane(300, 300, 0.05, 0.003, 11)
	addStreak(p, 300, 300, 45, 150, 255, 150, 0.4, 0.8) // 70% span ⇒ extends to image borders

	segs := DetectSegments(p, 300, 300, DefaultParams(5))
	require.Len(t, segs, 1)
	s := segs[0]
	streakSpan := spanOf(s, [][2]float64{{45, 150}, {255, 150}})
	assert.Greater(t, s.T1-s.T0, streakSpan)     // extended past the streak
	assert.GreaterOrEqual(t, s.T1-s.T0, 0.9*300) // ~full image width
}

func TestRefine_ExtentEndpointPad(t *testing.T) {
	p := noisyPlane(300, 300, 0.05, 0.003, 12)
	addStreak(p, 300, 300, 100, 150, 200, 150, 0.4, 0.8) // ~33% span ⇒ padded, not bordered

	segs := DetectSegments(p, 300, 300, DefaultParams(5))
	require.Len(t, segs, 1)
	s := segs[0]
	streakSpan := spanOf(s, [][2]float64{{100, 150}, {200, 150}})
	got := s.T1 - s.T0
	assert.Greater(t, got, streakSpan) // endpoint extension applied
	assert.Less(t, got, 0.9*300)       // but short of the full border extension
}

// spanOf projects the given points onto the segment's line direction and returns their extent.
func spanOf(s Segment, pts [][2]float64) float64 {
	tmin, tmax := 1e18, -1e18
	for _, pt := range pts {
		tp := s.project(pt[0], pt[1])
		if tp < tmin {
			tmin = tp
		}
		if tp > tmax {
			tmax = tp
		}
	}
	return tmax - tmin
}
