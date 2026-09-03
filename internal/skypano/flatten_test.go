package skypano

import (
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// bandCanvas builds a synthetic sky: a smooth dome plus a narrow Milky Way band on the galactic
// equator. Equirectangular galactic, so canvas y IS galactic latitude and the geometry is checkable
// by hand.
func bandCanvas(domeX, domeCurve, band float64) (*fits.Image, []float32, Canvas) {
	c := Canvas{Proj: Equirectangular, Fr: Galactic, W: 800, H: 800, Lon0: 60, Lat0: 0, ScaleDegPerPix: 0.15}
	im := fits.NewImage(c.W, c.H, 3)
	cov := make([]float32, c.W*c.H)
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			i := y*c.W + x
			u, v := uv(x, y, c.W, c.H)
			b := -v * (float64(c.H) / 2) * c.ScaleDegPerPix // galactic latitude at this row
			// The dome tilts both ways and curves across; it is kept flat ALONG each row so that
			// comparing rows measures the band and nothing else.
			val := 0.005 + domeX*u + 0.4*domeX*v + domeCurve*u*u + band*math.Exp(-(b/8)*(b/8))
			for ch := 0; ch < 3; ch++ {
				im.Pix[ch][i] = float32(val)
			}
			cov[i] = 1
		}
	}
	return im, cov, c
}

// rowMedian is the median of a canvas row, the statistic the band and dome are measured with.
func rowMedian(im *fits.Image, ch, y int) float64 {
	buf := make([]float64, im.W)
	for x := 0; x < im.W; x++ {
		buf[x] = float64(im.Pix[ch][y*im.W+x])
	}
	sort.Float64s(buf)
	return buf[len(buf)/2]
}

func colMedian(im *fits.Image, ch, x int) float64 {
	buf := make([]float64, im.H)
	for y := 0; y < im.H; y++ {
		buf[y] = float64(im.Pix[ch][y*im.W+x])
	}
	sort.Float64s(buf)
	return buf[len(buf)/2]
}

// TestFlatten_RemovesTheDomeAndKeepsTheBand is the property the whole file exists for. A background
// model that cannot tell the Milky Way from light pollution is worse than none at all.
func TestFlatten_RemovesTheDomeAndKeepsTheBand(t *testing.T) {
	const tilt, curve, band = 0.003, 0.002, 0.004
	im, cov, c := bandCanvas(tilt, curve, band)

	// Measure the band against the MEAN of two rows equally far above and below it, so a tilt in the
	// background cancels out of the measurement and only the band itself is being weighed.
	edge, centre := 40, im.H/2
	bandHeight := func() float64 {
		return rowMedian(im, 1, centre) - (rowMedian(im, 1, edge)+rowMedian(im, 1, im.H-1-edge))/2
	}
	// A tilt shows as a left-right difference; curvature shows as the edges sitting above the middle.
	tiltAcross := func() float64 { return colMedian(im, 1, im.W-40) - colMedian(im, 1, 40) }
	curveAcross := func() float64 {
		return (colMedian(im, 1, 40)+colMedian(im, 1, im.W-40))/2 - colMedian(im, 1, im.W/2)
	}
	bandBefore, tiltBefore, curveBefore := bandHeight(), tiltAcross(), curveAcross()
	require.InDelta(t, band, bandBefore, 1e-5, "the fixture should carry the band it claims")
	require.Greater(t, math.Abs(tiltBefore), 0.004, "the fixture should carry a tilt")
	require.Greater(t, math.Abs(curveBefore), 0.001, "the fixture should carry curvature")

	bg, err := Flatten(im, cov, c, DefaultFlattenOptions())
	require.NoError(t, err)
	assert.Equal(t, 2, bg.Order)
	assert.GreaterOrEqual(t, bg.Tiles, 4*numTerms(2),
		"enough tiles outside the band to support the order actually fitted")

	// A tenth, not a hundredth: each tile is summarised by a low percentile, which is a deliberately
	// biased estimate of its level (that is how stars are kept out of it), so the recovered surface
	// tracks the dome closely rather than exactly. What matters is that the residual is small
	// compared with the band the model has to leave alone.
	assert.InDelta(t, 0, tiltAcross(), 0.1*math.Abs(tiltBefore), "the tilt must be gone")
	assert.InDelta(t, 0, curveAcross(), 0.1*math.Abs(curveBefore), "the curvature must be gone")
	assert.InDelta(t, bandBefore, bandHeight(), 0.1*bandBefore, "the band must survive")
}

// TestFlatten_MaskedBandIsNotSampled: without the galactic mask the model is free to chase the band,
// which is the failure this design is built to avoid.
func TestFlatten_MaskedBandIsNotSampled(t *testing.T) {
	_, _, c := bandCanvas(0, 0, 0.004)
	im, cov, _ := bandCanvas(0, 0, 0.004)
	o := DefaultFlattenOptions()

	masked, _, _ := sampleTiles(im, cov, c, o, 0.5)
	o.MaskLatDeg = 0
	unmasked, _, _ := sampleTiles(im, cov, c, o, 0.5)

	assert.Less(t, len(masked), len(unmasked), "the mask must drop the band's tiles")
	assert.Greater(t, len(masked), 0)
}

func TestFlatten_StepsTheOrderDownRatherThanFitTooFewSamples(t *testing.T) {
	im, cov, c := bandCanvas(0.003, 0, 0.004)
	o := DefaultFlattenOptions()
	o.TilePx = 256 // few enough tiles that order 2 is not supported

	bg, err := Flatten(im, cov, c, o)
	require.NoError(t, err)
	assert.Less(t, bg.Order, 2, "an unsupported order must be stepped down, not fitted anyway")
}

func TestFlatten_RejectsAMismatchedCoverageMap(t *testing.T) {
	im, _, c := bandCanvas(0, 0, 0.004)

	_, err := Flatten(im, make([]float32, 7), c, DefaultFlattenOptions())

	require.Error(t, err)
}

func TestFitPoly_RecoversAKnownSurface(t *testing.T) {
	want := []float64{0.5, -0.25, 0.125, 0.4, -0.3, 0.2} // order 2, in polyTerms order
	var us, vs, vals []float64
	for i := 0; i <= 10; i++ {
		for j := 0; j <= 10; j++ {
			u, v := float64(i)/5-1, float64(j)/5-1
			us, vs = append(us, u), append(vs, v)
			vals = append(vals, evalPoly(want, u, v))
		}
	}

	got, ok := fitPoly(us, vs, vals, 2)

	require.True(t, ok)
	for i := range want {
		assert.InDelta(t, want[i], got[i], 1e-9, "term %d", i)
	}
}

func TestSolveDense_ReportsASingularSystem(t *testing.T) {
	a := [][]float64{{1, 2}, {2, 4}}

	_, ok := solveDense(a, []float64{1, 2})

	assert.False(t, ok, "a singular system must be refused, not solved approximately")
}

func TestTypicalCoverage_IgnoresTheEmptyCanvas(t *testing.T) {
	cov := make([]float32, 1000)
	for i := 0; i < 100; i++ {
		cov[i] = 2
	}

	assert.Equal(t, float32(2), TypicalCoverage(cov), "only covered pixels describe typical coverage")
}
