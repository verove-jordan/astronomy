package dither

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The planner's whole claim is that it beats random. These tests hold it to that: offsets must stay
// inside the box, spread out rather than cluster, cover sub-pixel phase, and survive a mount that
// does not do what it is told.

func TestPlanner_StaysInsideTheBox(t *testing.T) {
	p := NewPlanner(10)
	for i := 0; i < 40; i++ {
		_, target := p.Next()
		assert.LessOrEqual(t, math.Abs(target.X), 10.0)
		assert.LessOrEqual(t, math.Abs(target.Y), 10.0)
	}
}

func TestPlanner_SpreadsBetterThanRandom(t *testing.T) {
	const n = 24
	p := NewPlanner(10)
	planned := make([]Offset, 0, n)
	for i := 0; i < n; i++ {
		_, target := p.Next()
		planned = append(planned, target)
	}

	// A pseudo-random comparison drawn the way a naive implementation would.
	rng := &lcg{s: 42}
	random := make([]Offset, 0, n)
	for i := 0; i < n; i++ {
		random = append(random, Offset{X: (rng.next()*2 - 1) * 10, Y: (rng.next()*2 - 1) * 10})
	}

	assert.Greater(t, minPairDistance(planned), minPairDistance(random),
		"planned offsets must be further apart than random draws — clustering wastes a dither")
}

func TestPlanner_CoversSubPixelPhase(t *testing.T) {
	p := NewPlanner(8)
	var offsets []Offset
	for i := 0; i < 16; i++ {
		_, target := p.Next()
		offsets = append(offsets, target)
	}
	// Every quadrant of the unit pixel should be visited: that is what lets the stack recover
	// detail finer than one pixel.
	quadrants := map[[2]int]bool{}
	for _, o := range offsets {
		qx := int(math.Floor(positiveFrac(o.X) * 2))
		qy := int(math.Floor(positiveFrac(o.Y) * 2))
		quadrants[[2]int{qx, qy}] = true
	}
	assert.Len(t, quadrants, 4, "dithers must sample every sub-pixel quadrant")
}

func TestPlanner_NextIsARelativeMove(t *testing.T) {
	p := NewPlanner(10)
	delta1, target1 := p.Next()
	assert.InDelta(t, target1.X, delta1.X, 1e-9, "the first move starts from the origin")

	delta2, target2 := p.Next()
	assert.InDelta(t, target2.X-target1.X, delta2.X, 1e-9)
	assert.InDelta(t, target2.Y-target1.Y, delta2.Y, 1e-9)
}

// Backlash: a GEM asked for 8 px in declination may deliver 3. The planner must believe the frames,
// not the command, or its careful spread quietly becomes fiction.
func TestPlanner_AchievedCorrectsForBacklash(t *testing.T) {
	p := NewPlanner(10)
	_, target := p.Next()

	actual := Offset{X: target.X * 0.4, Y: target.Y * 0.4} // the mount under-delivered
	p.Achieved(actual)
	assert.Equal(t, actual, p.Current())

	delta, next := p.Next()
	assert.InDelta(t, next.X-actual.X, delta.X, 1e-9,
		"the next move must start from where the camera REALLY is")
	assert.InDelta(t, next.Y-actual.Y, delta.Y, 1e-9)

	used := p.Used()
	assert.Equal(t, actual, used[1], "history records the achieved offset, not the commanded one")
}

func TestPlanner_DefaultsToASaneRadius(t *testing.T) {
	p := NewPlanner(0)
	_, target := p.Next()
	assert.LessOrEqual(t, math.Abs(target.X), 10.0)
	assert.LessOrEqual(t, math.Abs(target.Y), 10.0)
}

// The planned pattern must be one the diagnostic in this same package calls "dithered" — the two
// halves have to agree, or the app would advise against its own behaviour.
func TestPlanner_ProducesAPatternTheDiagnosticApproves(t *testing.T) {
	p := NewPlanner(10)
	shifts := make([]Shift, 0, 20)
	for i := 0; i < 20; i++ {
		_, target := p.Next()
		shifts = append(shifts, Shift{X: target.X, Y: target.Y})
	}
	report := Analyze(shifts)
	require.NotNil(t, report)
	assert.Equal(t, "dithered", report.Pattern)
}

func minPairDistance(offsets []Offset) float64 {
	best := math.Inf(1)
	for i := 0; i < len(offsets); i++ {
		for j := i + 1; j < len(offsets); j++ {
			if d := math.Hypot(offsets[i].X-offsets[j].X, offsets[i].Y-offsets[j].Y); d < best {
				best = d
			}
		}
	}
	return best
}

func positiveFrac(v float64) float64 {
	f := v - math.Floor(v)
	if f < 0 {
		f += 1
	}
	return f
}
