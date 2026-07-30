package mosaic

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// TestAssembleChannel_RotatedRoundTrip proves the row-order convention end-to-end: a 30°-rotated
// panel with an asymmetric star pattern reprojects onto the north-up canvas with every star at
// exactly the pixel canvas.WCS.SkyToPix predicts. A y-flip anywhere between file rows and WCS
// axis 2 would mirror the off-center stars and miss by tens of pixels.
func TestAssembleChannel_RotatedRoundTrip(t *testing.T) {
	const w, h = 128, 128
	wcs := tanWCS(t, w, h, 100, -20, testScale, 30)
	sc := testScene{ra0: 100, dec0: -20, base: 0.1}
	for _, px := range [][2]float64{{30, 20}, {90, 40}, {50, 100}} {
		ra, dec := wcs.PixToSky(px[0], px[1])
		sc.stars = append(sc.stars, testStar{ra: ra, dec: dec, flux: 0.6})
	}
	rng := rand.New(rand.NewSource(3))
	panels := []PanelImage{{Label: "p01", Image: renderPanel(w, h, wcs, sc, 1, 0, 0, rng, 0), WCS: wcs}}

	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)
	out, rep, depth, err := AssembleChannel(context.Background(), panels, canvas, nil, Options{Workers: 2})
	require.NoError(t, err)
	require.Len(t, depth, canvas.W*canvas.H)
	assert.Greater(t, rep.CoveredFrac, 0.25)

	for _, st := range sc.stars {
		cx, cy, ok := canvas.WCS.SkyToPix(st.ra, st.dec)
		require.True(t, ok)
		px, py := peakNear(out.Pix[0], canvas.W, canvas.H, cx, cy, 3)
		assert.InDelta(t, cx, float64(px), 1.5, "star must land where the canvas WCS says")
		assert.InDelta(t, cy, float64(py), 1.5)
	}
}

// TestAssembleChannel_EndToEnd2x2 is the full pipeline over a synthetic 2×2 mosaic: real TAN
// WCS, 20% overlap, sky gradient + star grid + edge-elongated optics + injected photometry +
// noise; then PlanCanvas → FitPhotometry → AssembleChannel.
func TestAssembleChannel_EndToEnd2x2(t *testing.T) {
	const w, h = 256, 256
	const ov = 0.2
	const noise = 0.003
	ctx := context.Background()
	sc := testScene{ra0: 180, dec0: 30, base: 0.12, gxi: 0.25, geta: 0.35}
	wcss := gridWCS(t, 180, 30, 2, 2, w, h, ov)

	// Star grid pinned to sky, spaced so no star sits near a panel-center background window.
	for _, u := range []float64{-200, -120, -40, 40, 120, 200} {
		for _, v := range []float64{-200, -120, -40, 40, 120, 200} {
			ra, dec := astro.TangentSky(sc.ra0, sc.dec0, u*testScale, v*testScale)
			sc.stars = append(sc.stars, testStar{ra: ra, dec: dec, flux: 0.45})
		}
	}
	// The roundness probe: near panel 0's right edge (elongated there), interior to panel 1.
	sRA, sDec := wcss[0].PixToSky(246.5, 120)
	kept := sc.stars[:0]
	for _, st := range sc.stars {
		if astro.AngularSeparation(st.ra, st.dec, sRA, sDec) > 12*testScale {
			kept = append(kept, st)
		}
	}
	sc.stars = append(kept, testStar{ra: sRA, dec: sDec, flux: 0.45})
	sx1, sy1, ok := wcss[1].SkyToPix(sRA, sDec)
	require.True(t, ok)
	require.Greater(t, sx1, 31.0, "probe star must be interior (round) in panel 1")
	require.Less(t, sx1, float64(w)-31)
	require.Greater(t, sy1, 31.0)
	require.Less(t, sy1, float64(h)-31)

	gains := []float64{1.06, 0.92, 1.10, 0.95}
	offsets := []float64{0.010, -0.015, 0.020, -0.010}
	rng := rand.New(rand.NewSource(42))
	panels := make([]PanelImage, 0, 4)
	for i, wcs := range wcss {
		panels = append(panels, PanelImage{
			Label: labelN(i),
			Image: renderPanel(w, h, wcs, sc, gains[i], offsets[i], noise, rng, 30),
			WCS:   wcs,
		})
	}

	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)
	expected := float64(w) + (1-ov)*float64(w) + 2*canvasPadPx
	assert.InDelta(t, expected, float64(canvas.W), 2, "canvas width")
	assert.InDelta(t, expected, float64(canvas.H), 2, "canvas height")

	sol, err := FitPhotometry(ctx, panels, canvas, "gain_offset", 4)
	require.NoError(t, err)
	for i := range panels {
		want := gains[sol.Anchor] / gains[i]
		assert.InDelta(t, want, sol.Gain[i], 0.02*want, "panel %d gain", i)
	}

	out, rep, depth, err := AssembleChannel(ctx, panels, canvas, sol, Options{Workers: 4})
	require.NoError(t, err)

	// (a) coverage: all four panels placed, most of the canvas covered, overlaps deeper than 1.
	require.Len(t, rep.Panels, 4)
	assert.Equal(t, []string{"p01", "p02", "p03", "p04"}, placementLabels(rep.Panels))
	assert.Greater(t, rep.CoveredFrac, 0.70)
	assert.LessOrEqual(t, rep.CoveredFrac, 0.95)
	maxDepth := float32(0)
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	assert.Greater(t, maxDepth, float32(1.05), "overlaps must accumulate more than one panel's weight")
	for _, pl := range rep.Panels {
		assert.GreaterOrEqual(t, pl.X0, 0)
		assert.Less(t, pl.X0, pl.X1)
		assert.LessOrEqual(t, pl.X1, canvas.W)
		assert.Less(t, pl.Y0, pl.Y1)
	}

	// (b) background flatness: each panel-center region must sit on the anchor's instrumental
	// scale within 2× the injected noise sigma.
	for i, p := range panels {
		ra, dec := p.WCS.PixToSky(float64(w-1)/2, float64(h-1)/2)
		cx, cy, ok := canvas.WCS.SkyToPix(ra, dec)
		require.True(t, ok)
		med := medianWindow(out.Pix[0], canvas.W, canvas.H, int(math.Round(cx)), int(math.Round(cy)), 10)
		truth := sc.skyAt(ra, dec)*gains[sol.Anchor] + offsets[sol.Anchor]
		assert.InDelta(t, truth, med, 2*noise, "panel %d center background", i)
	}

	// (c) seam residual stays at the noise floor.
	assert.Greater(t, rep.SeamRMS, 0.0)
	assert.Less(t, rep.SeamRMS, 1.5*noise)

	// (d) the overlap star: elongated as panel 0 rendered it, round after the blend, because the
	// center-weighted feather hands the pixel to panel 1.
	elong := axisRatioAt(panels[0].Image.Pix[0], w, h, 246.5, 120)
	require.Less(t, elong, 0.75, "panel 0 must have rendered the probe star elongated")
	bx, by, ok := canvas.WCS.SkyToPix(sRA, sDec)
	require.True(t, ok)
	blend := axisRatioAt(out.Pix[0], canvas.W, canvas.H, bx, by)
	assert.Greater(t, blend, elong+0.15, "blended star must be rounder than the elongated rendering")
	assert.Greater(t, blend, 0.8, "blended star must be near-round")
}

func TestAssembleChannel_InputValidation(t *testing.T) {
	const w, h = 64, 64
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	panels := scenePanels(t, gridWCS(t, 150, 20, 1, 1, w, h, 0.2), w, h, sc)
	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)

	_, _, _, err = AssembleChannel(context.Background(), nil, canvas, nil, Options{})
	assert.Error(t, err)
	_, _, _, err = AssembleChannel(context.Background(), panels, CanvasSpec{}, nil, Options{})
	assert.Error(t, err)
	_, _, _, err = AssembleChannel(context.Background(), panels, canvas, &PhotomSolution{Gain: []float64{1, 1}, Offset: []float64{0, 0}}, Options{})
	assert.Error(t, err, "solution/panel count mismatch must error")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err = AssembleChannel(ctx, panels, canvas, nil, Options{})
	assert.Error(t, err, "a canceled context must abort the accumulate loop")
}

func placementLabels(pls []PanelPlacement) []string {
	out := make([]string, len(pls))
	for i, p := range pls {
		out[i] = p.Label
	}
	return out
}
