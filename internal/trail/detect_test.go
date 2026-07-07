package trail

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectSegments_NoTrail(t *testing.T) {
	p := noisyPlane(300, 300, 0.1, 0.005, 1)
	assert.Empty(t, DetectSegments(p, 300, 300, DefaultParams(5)))
}

func TestDetectSegments_SingleDiagonal(t *testing.T) {
	p := noisyPlane(300, 300, 0.1, 0.004, 2)
	addStreak(p, 300, 300, 40, 40, 260, 260, 0.1, 0.8) // 45°, ~2px FWHM, ~25σ bright

	segs := DetectSegments(p, 300, 300, DefaultParams(5))
	require.Len(t, segs, 1)
	s := segs[0]
	assert.InDelta(t, 45, lineAngleDeg(s), 3) // orientation within ±3°
	assert.Greater(t, s.T1-s.T0, 0.25*300)    // spans ≥25% of the frame
	assert.Positive(t, s.Width)
}

func TestDetectSegments_TwoCrossing(t *testing.T) {
	p := noisyPlane(300, 300, 0.1, 0.004, 3)
	addStreak(p, 300, 300, 40, 40, 260, 260, 0.1, 0.8) // +45°
	addStreak(p, 300, 300, 40, 260, 260, 40, 0.1, 0.8) // -45°

	segs := DetectSegments(p, 300, 300, DefaultParams(5))
	require.Len(t, segs, 2)
	// The two segments are near-orthogonal in orientation.
	da := math.Abs(lineAngleDeg(segs[0]) - lineAngleDeg(segs[1]))
	assert.InDelta(t, 90, da, 6)
}

func TestDetectSegments_TexturedField(t *testing.T) {
	p := noisyPlane(300, 300, 0.1, 0.004, 4)
	rng := rand.New(rand.NewSource(5))
	for i := range p {
		if rng.Float64() < 0.4 { // >30% of cells bright ⇒ rejected outright
			p[i] += 0.1
		}
	}
	assert.Empty(t, DetectSegments(p, 300, 300, DefaultParams(5)))
}

func TestDetectSegments_ShortStreak(t *testing.T) {
	p := noisyPlane(300, 300, 0.1, 0.004, 6)
	addStreak(p, 300, 300, 130, 130, 170, 170, 0.1, 0.8) // ~56px span < 25% of 300
	assert.Empty(t, DetectSegments(p, 300, 300, DefaultParams(5)))
}

func TestDetectSegments_Degenerate(t *testing.T) {
	assert.Nil(t, DetectSegments(nil, 0, 0, DefaultParams(5)))
	assert.Nil(t, DetectSegments(make([]float32, 10), 5, 2, DefaultParams(5)))      // too small
	assert.Nil(t, DetectSegments(make([]float32, 100), 40, 40, DefaultParams(5)))   // size mismatch
	assert.Nil(t, DetectSegments(make([]float32, 40*40), 40, 40, DefaultParams(5))) // flat ⇒ sigma 0
}
