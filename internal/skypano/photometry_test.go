package skypano

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planeSamples builds paired samples following target = gain*val + a + b*u + c*v.
func planeSamples(gain float64, plane [3]float64, n int) (val, us, vs, target []float64) {
	for i := 0; i < n; i++ {
		u := float64(i%20)/10 - 1
		v := float64(i/20)/float64(n/40) - 1
		x := 0.004 + 0.002*math.Sin(float64(i))
		val = append(val, x)
		us, vs = append(us, u), append(vs, v)
		target = append(target, gain*x+plane[0]+plane[1]*u+plane[2]*v)
	}
	return
}

func TestFitPlane_RecoversALevelAndATilt(t *testing.T) {
	want := [3]float64{0.0005, -0.0003, 0.0002}
	val, us, vs, target := planeSamples(1, want, 800)

	plane, ok := fitPlane(val, us, vs, target)

	require.True(t, ok)
	for k := 0; k < 3; k++ {
		assert.InDelta(t, want[k], plane[k], 1e-5, "plane term %d", k)
	}
}

// TestFitPlane_SurvivesOutliers: stars land in both panels of an overlap but on slightly different
// pixels, so a fit that a handful of samples can tilt would put a gradient across the whole panel.
func TestFitPlane_SurvivesOutliers(t *testing.T) {
	want := [3]float64{0.0004, 0.0002, -0.0001}
	val, us, vs, target := planeSamples(1, want, 800)
	for i := 0; i < len(target); i += 40 {
		target[i] += 0.5 // a star, two orders of magnitude above the sky
	}

	plane, ok := fitPlane(val, us, vs, target)

	require.True(t, ok)
	for k := 0; k < 3; k++ {
		assert.InDelta(t, want[k], plane[k], 1e-4, "plane term %d", k)
	}
}

// TestFitPlane_IsUnmovedByAGainDifference records why there is no gain here. A panel that is
// uniformly 20 per cent brighter is absorbed as a level, which is what the additive model can say —
// and on gain-normalised ProRAW that difference is a few per cent, not twenty.
func TestFitPlane_IsUnmovedByAGainDifference(t *testing.T) {
	val, us, vs, target := planeSamples(1.2, [3]float64{0, 0, 0}, 800)

	plane, ok := fitPlane(val, us, vs, target)

	require.True(t, ok)
	var sum float64
	for _, x := range val {
		sum += x
	}
	assert.InDelta(t, 0.2*sum/float64(len(val)), plane[0], 1e-4, "the level absorbs it")
	assert.InDelta(t, 0, plane[1], 1e-4, "and it does not become a tilt")
	assert.InDelta(t, 0, plane[2], 1e-4)
}

func TestFitPlane_RefusesTooFewSamples(t *testing.T) {
	val, us, vs, target := planeSamples(1, [3]float64{0, 0, 0}, 16)

	_, ok := fitPlane(val, us, vs, target)

	assert.False(t, ok)
}

func TestCorrection_ZeroValueIsTheIdentity(t *testing.T) {
	var co Correction

	assert.Equal(t, 0.0042, co.Apply(0, 0.0042, 0.5, -0.5),
		"an unmatched panel must render as itself")
}

func TestCorrection_AppliesThePlaneAtThePanelPosition(t *testing.T) {
	co := Correction{Plane: [][3]float64{{0.1, 0.01, 0.001}}}

	assert.InDelta(t, 0.5+0.1+0.01*1+0.001*-1, co.Apply(0, 0.5, 1, -1), 1e-12)
}

func TestPanelUV_MapsTheFrameToPlusMinusOne(t *testing.T) {
	tests := []struct {
		name string
		x, y float64
		u, v float64
	}{
		{"centre", 2016, 1512, 0, 0},
		{"top left", 0, 0, -1, -1},
		{"bottom right", 4032, 3024, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, v := panelUV(tt.x, tt.y, 4032, 3024)

			assert.InDelta(t, tt.u, u, 1e-12, "u")
			assert.InDelta(t, tt.v, v, 1e-12, "v")
		})
	}
}

// TestFitPlane_StaysAPlane pins a decision that cost a run to learn. The assembled canvas carries a
// smooth 52%-peak-to-peak colour field, and the physics says it should be curved — airglow follows
// sec(z) across a 72-degree panel. Fitting u², uv and v² anyway did not remove it: measured on the
// arch canvas, (R-G)/G went 0.716 → 0.693 while (B-G)/G went 0.580 → 0.852, i.e. blue got half again
// worse. The samples are OVERLAPS, which sit at the panel EDGES, so a quadratic is unconstrained
// across the panel interior and extrapolates curvature nothing measured into the middle of the frame.
// The correction stays rigid; see the file header.
func TestFitPlane_StaysAPlane(t *testing.T) {
	co := identityCorrection(1)
	require.Len(t, co.Plane, 1)
	assert.Len(t, co.Plane[0], 3, "the correction is a level and two tilts — no curvature terms")

	// And a curved difference is deliberately only PARTLY absorbed: the plane takes the level and the
	// tilt and leaves the curvature, rather than chasing it into the panel interior where no overlap
	// sample ever constrained the answer.
	var val, us, vs, target []float64
	for i := 0; i < 800; i++ {
		u := float64(i%20)/10 - 1
		v := float64(i/20)/20 - 1
		x := 0.004 + 0.002*math.Sin(float64(i))
		val, us, vs = append(val, x), append(us, u), append(vs, v)
		target = append(target, x+0.0005+0.0006*u*u)
	}
	plane, ok := fitPlane(val, us, vs, target)
	require.True(t, ok)
	const curvature = 0.0006
	assert.Less(t, math.Abs(plane[1]), 0.2*curvature, "curvature must not turn into a large tilt")
	assert.Less(t, math.Abs(plane[2]), 0.2*curvature)
}
