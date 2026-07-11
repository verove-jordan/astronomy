package dither

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lcg struct{ s uint64 }

func (l *lcg) next() float64 { // deterministic uniform [0,1)
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>11) / float64(1<<53)
}

func TestAnalyze_TooFewFrames(t *testing.T) {
	assert.Nil(t, Analyze(make([]Shift, 4)))
}

func TestAnalyze_Static(t *testing.T) {
	// Tight tracking, no dithering: sub-pixel jitter only.
	rng := &lcg{s: 1}
	shifts := make([]Shift, 20)
	for i := range shifts {
		shifts[i] = Shift{X: (rng.next() - 0.5) * 0.6, Y: (rng.next() - 0.5) * 0.6}
	}
	r := Analyze(shifts)
	require.NotNil(t, r)
	assert.Equal(t, "static", r.Pattern)
	assert.True(t, r.WalkingNoiseRisk())
	assert.Contains(t, r.Note, "dithering")
}

func TestAnalyze_LinearDrift(t *testing.T) {
	// The user's case: unguided drift, ~(2,1) px per frame, small jitter — walking-noise territory.
	rng := &lcg{s: 2}
	shifts := make([]Shift, 30)
	for i := range shifts {
		shifts[i] = Shift{
			X: 2*float64(i) + (rng.next()-0.5)*0.2,
			Y: 1*float64(i) + (rng.next()-0.5)*0.2,
		}
	}
	r := Analyze(shifts)
	require.NotNil(t, r)
	assert.Equal(t, "drift", r.Pattern)
	assert.True(t, r.WalkingNoiseRisk())
	assert.InDelta(t, 2.24, r.DriftPxPerFrame, 0.3, "recovers the planted √5 px/frame rate")
	assert.GreaterOrEqual(t, r.DirectionR, 0.8)
	assert.Contains(t, r.Note, "walking-noise")
}

func TestAnalyze_Dithered(t *testing.T) {
	// Random ~±10 px offsets: the ideal capture pattern.
	rng := &lcg{s: 3}
	shifts := make([]Shift, 25)
	for i := range shifts {
		shifts[i] = Shift{X: (rng.next() - 0.5) * 20, Y: (rng.next() - 0.5) * 20}
	}
	r := Analyze(shifts)
	require.NotNil(t, r)
	assert.Equal(t, "dithered", r.Pattern)
	assert.False(t, r.WalkingNoiseRisk())
	assert.GreaterOrEqual(t, r.StepMedianPx, 2.0)
	assert.LessOrEqual(t, r.DirectionR, 0.5)
}

func TestAnalyze_MixedSlowWander(t *testing.T) {
	// A slow random walk (1 px steps, random directions): the cloud moves (> static span) but the
	// steps are too small to be deliberate dithering.
	rng := &lcg{s: 4}
	cur := Shift{}
	shifts := make([]Shift, 24)
	for i := range shifts {
		shifts[i] = cur
		dx, dy := rng.next()-0.5, rng.next()-0.5
		n := 1.0 / math.Hypot(dx, dy)
		cur = Shift{X: cur.X + dx*n, Y: cur.Y + dy*n}
	}
	r := Analyze(shifts)
	require.NotNil(t, r)
	assert.Equal(t, "mixed", r.Pattern)
	assert.False(t, r.WalkingNoiseRisk())
}

func TestAnalyze_SessionJumpDoesNotMaskDrift(t *testing.T) {
	// Two drifting sessions merged: the one-off 400 px pointing jump at the boundary must be
	// excluded from the step statistics, so the drift verdict survives.
	shifts := make([]Shift, 0, 30)
	for i := 0; i < 15; i++ {
		shifts = append(shifts, Shift{X: 2 * float64(i), Y: float64(i)})
	}
	for i := 0; i < 15; i++ {
		shifts = append(shifts, Shift{X: 400 + 2*float64(i), Y: 300 + float64(i)})
	}
	r := Analyze(shifts)
	require.NotNil(t, r)
	assert.Equal(t, "drift", r.Pattern)
	assert.InDelta(t, 2.24, r.DriftPxPerFrame, 0.3)
}

func TestWalkingNoiseRisk_NilSafe(t *testing.T) {
	var r *Report
	assert.False(t, r.WalkingNoiseRisk())
}
