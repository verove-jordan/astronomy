package skypano

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// distortedPanel builds a synthetic panel: a camera with known distortion, a grid of sky directions
// covering the WHOLE field, and the pixels that camera puts them at.
func distortedPanel(truth Camera) ([][3]float64, []Detection) {
	var cat [][3]float64
	var det []Detection
	for iy := 0; iy <= 30; iy++ {
		for ix := 0; ix <= 40; ix++ {
			x := float64(ix) / 40 * 4031
			y := float64(iy) / 30 * 3023
			v := truth.Unproject(x, y)
			px, py, ok := truth.Project(v)
			if !ok {
				continue
			}
			cat = append(cat, v)
			det = append(det, Detection{X: px, Y: py})
		}
	}
	return cat, det
}

func testCamera(k1, k2, k3 float64) Camera {
	return Camera{
		R:  SetRotation([3]float64{1, 0, 0}, [3]float64{0, 0, -1}, [3]float64{0, 1, 0}),
		F:  2810,
		K1: k1, K2: k2, K3: k3,
		Cx: 2016, Cy: 1512,
	}
}

func TestCamera_RoundTripsThroughTheDistortion(t *testing.T) {
	c := testCamera(0.08, -0.05, 0.01)

	for _, p := range [][2]float64{{2016, 1512}, {0, 0}, {4031, 0}, {0, 3023}, {4031, 3023}, {3000, 500}} {
		v := c.Unproject(p[0], p[1])
		x, y, ok := c.Project(v)

		require.True(t, ok, "pixel %v", p)
		assert.InDelta(t, p[0], x, 1e-6, "x at %v", p)
		assert.InDelta(t, p[1], y, 1e-6, "y at %v", p)
	}
}

// TestBundleLens_RecoversASharedDistortion is the property the mosaic depends on. Every panel is
// given the SAME wrong lens (no distortion at all) and a rotation error, and the bundle has to find
// the distortion from the stars.
func TestBundleLens_RecoversASharedDistortion(t *testing.T) {
	truth := testCamera(0.06, -0.04, 0)
	var cams []Camera
	var cats [][][3]float64
	var dets [][]Detection
	for i, tilt := range []float64{0, 0.004, -0.003} {
		cat, det := distortedPanel(truth)
		// The starting guess: right focal length, no distortion, slightly wrong pointing.
		start := truth
		start.K1, start.K2, start.K3 = 0, 0, 0
		start.R = Rotate(truth.R, tilt, -tilt, tilt/2)
		cams = append(cams, start)
		cats = append(cats, cat)
		dets = append(dets, det)
		_ = i
	}

	o := DefaultSolveOptions()
	o.MatchRadiusPx, o.FitRadiusPx = 120, 2
	got, sols, ok := BundleLens(cams, cats, dets, o, 8)

	require.True(t, ok)
	for i, s := range sols {
		assert.Greater(t, s.Matches, 800, "panel %d should keep most of its stars", i)
		assert.Less(t, s.RMSPx, 0.5, "panel %d residual", i)
	}
	// The recovered lens is the shared one, and it is the same for every panel by construction.
	for i := range got {
		assert.InDelta(t, truth.K1, got[i].K1, 0.01, "K1 on panel %d", i)
		assert.InDelta(t, truth.K2, got[i].K2, 0.02, "K2 on panel %d", i)
		assert.InDelta(t, truth.F, got[i].F, 30, "F on panel %d", i)
	}
}

// TestBundleLens_PanelsAgreeAfterwards checks the thing the pictures actually need: two panels shown
// the same sky direction must agree on WHERE ON THE SKY it is. Disagreement is what the blend
// averages into a dash, and it is invisible in either panel's own residual.
func TestBundleLens_PanelsAgreeAfterwards(t *testing.T) {
	truthA := testCamera(0.06, -0.04, 0)
	truthB := truthA
	truthB.R = Rotate(truthA.R, 0.05, 0.02, 0) // about 3 degrees away, so the overlap is wide
	truths := []Camera{truthA, truthB}

	var cams []Camera
	var cats [][][3]float64
	var dets [][]Detection
	for _, tr := range truths {
		cat, det := distortedPanel(tr)
		start := tr // right pointing and focal length, but the lens taken as perfect
		start.K1, start.K2, start.K3 = 0, 0, 0
		cams = append(cams, start)
		cats = append(cats, cat)
		dets = append(dets, det)
	}

	o := DefaultSolveOptions()
	o.MatchRadiusPx, o.FitRadiusPx = 120, 2

	before := skyDisagreementArcsec(truths, cams)
	got, _, ok := BundleLens(cams, cats, dets, o, 8)
	require.True(t, ok)
	after := skyDisagreementArcsec(truths, got)

	// For scale: the real session measured about 560 arcsec of disagreement between panels, and the
	// mosaic canvas is 108 arcsec per pixel — so anything under ~50 arcsec is sub-pixel and cannot
	// draw a dash. This fixture is a milder version of the same effect.
	assert.Greater(t, before, 100.0, "an unmodelled lens must show up as panels disagreeing")
	assert.Less(t, after, 20.0, "after the bundle the panels must place a star together")
	t.Logf("panel disagreement %.0f arcsec -> %.0f arcsec", before, after)
}

// skyDisagreementArcsec is the median angle between where two panels think the same star is. Each
// star's TRUE pixel in each panel comes from that panel's true camera; the estimated cameras then
// map those pixels back to the sky, and the two answers should coincide.
func skyDisagreementArcsec(truths, est []Camera) float64 {
	var seps []float64
	for iy := 1; iy < 20; iy++ {
		for ix := 1; ix < 20; ix++ {
			x, y := float64(ix)/20*4031, float64(iy)/20*3023
			v := truths[0].Unproject(x, y)
			xb, yb, ok := truths[1].Project(v)
			if !ok || xb < 0 || yb < 0 || xb > 4031 || yb > 3023 {
				continue // not in the overlap
			}
			va := est[0].Unproject(x, y)
			vb := est[1].Unproject(xb, yb)
			seps = append(seps, math.Acos(clamp1(dot3(va, vb)))*180/math.Pi*3600)
		}
	}
	if len(seps) == 0 {
		return 0
	}
	return quantileOf(seps, 0.5)
}

func TestCamera_RadialCorrInterpolates(t *testing.T) {
	c := testCamera(0, 0, 0)
	c.RadialCorr = []float64{0, 0.01, 0.02}
	c.RadialCorrMaxR = 1.0

	tests := []struct {
		name string
		r    float64
		want float64
	}{
		{"at the centre", 0, 0},
		{"on a sample", 0.5, 0.01},
		{"between samples", 0.25, 0.005},
		{"at the last sample", 1.0, 0.02},
		{"beyond the table it holds", 1.5, 0.02},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, c.corrAt(tt.r), 1e-12)
		})
	}
}

func TestCamera_RoundTripsThroughTheRadialTable(t *testing.T) {
	// Unproject inverts the factor by iteration, so the table must round-trip like the polynomial.
	c := testCamera(0.06, -0.04, 0)
	c.RadialCorr = []float64{0, 0.001, 0.004, 0.012}
	c.RadialCorrMaxR = 0.95

	for _, p := range [][2]float64{{2016, 1512}, {0, 0}, {4031, 3023}, {3800, 200}} {
		v := c.Unproject(p[0], p[1])
		x, y, ok := c.Project(v)

		require.True(t, ok, "pixel %v", p)
		assert.InDelta(t, p[0], x, 1e-4, "x at %v", p)
		assert.InDelta(t, p[1], y, 1e-4, "y at %v", p)
	}
}

// TestFitRadialTable_RecoversAnEdgeOnlyError is the shape the polynomial could not make: flat across
// the inner field, then rising steeply at the corners.
func TestFitRadialTable_RecoversAnEdgeOnlyError(t *testing.T) {
	truth := testCamera(0, 0, 0)
	truth.RadialCorr = make([]float64, 20)
	truth.RadialCorrMaxR = 0.9
	for k := range truth.RadialCorr {
		if r := float64(k) / 19 * 0.9; r > 0.6 {
			truth.RadialCorr[k] = 0.004 * (r - 0.6) / 0.3 // nothing until 0.6, then a ramp
		}
	}
	cat, det := distortedPanel(truth)

	// The estimate knows nothing of the table; the matches are the truth's pixels.
	est := testCamera(0, 0, 0)
	ms := make([]Match, len(cat))
	for i := range cat {
		ms[i] = Match{Vec: cat[i], X: det[i].X, Y: det[i].Y}
	}
	before := maxRadialResidual(est, ms)

	table, maxR := fitRadialTable([]Camera{est}, [][]Match{ms}, 20)
	require.NotEmpty(t, table)
	est.RadialCorr, est.RadialCorrMaxR = table, maxR
	after := maxRadialResidual(est, ms)

	assert.Greater(t, before, 5.0, "the fixture should start with a real edge error")
	assert.Less(t, after, before/5, "the table must absorb most of it")
	t.Logf("max radial residual %.2f px -> %.2f px", before, after)
}

// maxRadialResidual is the largest along-radius reprojection error over the matches.
func maxRadialResidual(c Camera, ms []Match) float64 {
	worst := 0.0
	for _, m := range ms {
		x, y, ok := c.Project(m.Vec)
		if !ok {
			continue
		}
		dx, dy := x-c.Cx, y-c.Cy
		r := math.Hypot(dx, dy)
		if r < 1 {
			continue
		}
		worst = math.Max(worst, math.Abs(((m.X-x)*dx+(m.Y-y)*dy)/r))
	}
	return worst
}
