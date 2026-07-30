package mosaic

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// photomGrid renders a 3×3 mosaic of sky-only panels with injected per-panel gains/offsets.
func photomGrid(t *testing.T, gains, offsets []float64) ([]PanelImage, CanvasSpec) {
	t.Helper()
	const w, h = 128, 128
	const ov = 0.25
	// Gradient strong enough that even the small corner overlaps carry usable gain information
	// (the fit's precision scales with band dynamic range × √samples).
	sc := testScene{ra0: 150, dec0: 20, base: 0.2, gxi: 0.9, geta: 0.7}
	rng := rand.New(rand.NewSource(11))
	wcss := gridWCS(t, 150, 20, 3, 3, w, h, ov)
	panels := make([]PanelImage, 0, 9)
	for i, wcs := range wcss {
		panels = append(panels, PanelImage{
			Label: labelN(i),
			Image: renderPanel(w, h, wcs, sc, gains[i], offsets[i], 0.001, rng, 0),
			WCS:   wcs,
		})
	}
	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)
	return panels, canvas
}

func TestFitPhotometry_RecoversInjectedGrid(t *testing.T) {
	gains := []float64{1.00, 0.92, 1.08, 0.95, 1.10, 0.90, 1.05, 0.97, 1.03}
	offsets := []float64{0.010, -0.015, 0.020, -0.010, 0.015, -0.020, 0.005, 0.018, -0.012}
	panels, canvas := photomGrid(t, gains, offsets)

	sol, err := FitPhotometry(context.Background(), panels, canvas, "gain_offset", 4)
	require.NoError(t, err)
	require.Len(t, sol.Gain, 9)
	assert.GreaterOrEqual(t, len(sol.Pairs), 12, "every adjacent overlap must be measured")
	assert.Empty(t, sol.Warnings)
	assert.Equal(t, 1.0, sol.Gain[sol.Anchor])
	assert.Equal(t, 0.0, sol.Offset[sol.Anchor])

	for i := range panels {
		wantGain := gains[sol.Anchor] / gains[i]
		assert.InDelta(t, wantGain, sol.Gain[i], 0.01*wantGain, "panel %d gain within 1%%", i)
		// corrected_i must land on the anchor's instrumental scale: g·o_inj + b ≈ o_anchor_inj.
		assert.InDelta(t, offsets[sol.Anchor], sol.Gain[i]*offsets[i]+sol.Offset[i], 0.003, "panel %d offset", i)
	}
}

func TestFitPhotometry_Modes(t *testing.T) {
	gains := []float64{1, 1.1, 0.9, 1.05, 0.95, 1, 1.02, 0.98, 1}
	offsets := []float64{0, 0.01, -0.01, 0.02, -0.02, 0.005, 0, 0.01, -0.005}
	panels, canvas := photomGrid(t, gains, offsets)

	t.Run("off is identity", func(t *testing.T) {
		sol, err := FitPhotometry(context.Background(), panels, canvas, "off", 2)
		require.NoError(t, err)
		assert.Empty(t, sol.Pairs)
		for i := range panels {
			assert.Equal(t, 1.0, sol.Gain[i])
			assert.Equal(t, 0.0, sol.Offset[i])
		}
	})
	t.Run("offset keeps unit gains", func(t *testing.T) {
		sol, err := FitPhotometry(context.Background(), panels, canvas, "offset", 2)
		require.NoError(t, err)
		require.NotEmpty(t, sol.Pairs)
		for _, pr := range sol.Pairs {
			assert.True(t, pr.OffsetOnly)
			assert.Equal(t, 1.0, pr.Gain)
		}
		for i := range panels {
			assert.Equal(t, 1.0, sol.Gain[i])
		}
	})
	t.Run("unknown mode errors", func(t *testing.T) {
		_, err := FitPhotometry(context.Background(), panels, canvas, "bogus", 2)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown photometric mode")
	})
}

func TestPhotomSolution_RefitOffsets(t *testing.T) {
	gains := []float64{1.00, 0.92, 1.08, 0.95, 1.10, 0.90, 1.05, 0.97, 1.03}
	offA := []float64{0.010, -0.015, 0.020, -0.010, 0.015, -0.020, 0.005, 0.018, -0.012}
	panelsA, canvas := photomGrid(t, gains, offA)
	sol, err := FitPhotometry(context.Background(), panelsA, canvas, "gain_offset", 4)
	require.NoError(t, err)

	// Channel B: same per-panel gains (shared optics/exposure), different sky pedestals.
	offB := []float64{-0.005, 0.022, -0.018, 0.012, -0.008, 0.025, -0.015, 0.007, 0.019}
	const w, h = 128, 128
	sc := testScene{ra0: 150, dec0: 20, base: 0.11, gxi: 0.3, geta: 0.5}
	rng := rand.New(rand.NewSource(13))
	panelsB := make([]PanelImage, 0, 9)
	for i, p := range panelsA {
		panelsB = append(panelsB, PanelImage{
			Label: p.Label,
			Image: renderPanel(w, h, p.WCS, sc, gains[i], offB[i], 0.001, rng, 0),
			WCS:   p.WCS,
		})
	}
	before := append([]float64(nil), sol.Offset...)
	refit, err := sol.RefitOffsets(context.Background(), panelsB, canvas, 4)
	require.NoError(t, err)
	assert.Equal(t, before, sol.Offset, "the input solution must be untouched")
	assert.Equal(t, sol.Gain, refit.Gain, "gains are carried, not refit")
	for i := range panelsB {
		assert.InDelta(t, offB[refit.Anchor], refit.Gain[i]*offB[i]+refit.Offset[i], 0.003, "panel %d channel-B offset", i)
	}
}

func TestFitPhotometry_DisconnectedIslands(t *testing.T) {
	const w, h = 96, 96
	rng := rand.New(rand.NewSource(5))
	sc := testScene{ra0: 150, dec0: 20, base: 0.12, gxi: 0.2, geta: 0.2}
	mk := func(label string, dec, gain, offset float64) PanelImage {
		wcs := tanWCS(t, w, h, 150, dec, testScale, 0)
		return PanelImage{Label: label, Image: renderPanel(w, h, wcs, sc, gain, offset, 0.001, rng, 0), WCS: wcs}
	}
	// Panels 0,1 overlap around dec 20; panels 2,3 overlap 2 degrees north — no cross-overlap.
	off := 0.7 * float64(w) * testScale
	panels := []PanelImage{
		mk("p01", 20, 1.0, 0.01),
		mk("p02", 20+off, 1.05, -0.01),
		mk("p03", 22, 1.0, 0.06),
		mk("p04", 22+off, 1.0, 0.05),
	}
	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)

	sol, err := FitPhotometry(context.Background(), panels, canvas, "gain_offset", 2)
	require.NoError(t, err)
	require.NotEmpty(t, sol.Warnings)
	assert.Contains(t, sol.Warnings[0], "p03")
	assert.Contains(t, sol.Warnings[0], "p04")
	assert.Contains(t, []int{0, 1}, sol.Anchor, "anchor must sit in the first component")
	// Islands: gain forced to 1, sky leveled onto the anchor component's corrected median sky.
	var anchorSkies []float64
	for _, i := range []int{0, 1} {
		anchorSkies = append(anchorSkies, sol.Gain[i]*skyOf(panels[i].Image)+sol.Offset[i])
	}
	ref := medianF64(anchorSkies)
	for _, i := range []int{2, 3} {
		assert.Equal(t, 1.0, sol.Gain[i])
		assert.InDelta(t, ref, skyOf(panels[i].Image)+sol.Offset[i], 1e-9, "island %d sky-leveled", i)
	}
}

func TestSolveGains_LoopClosureBeatsChain(t *testing.T) {
	// A 4-panel ring with one drifted gain measurement on the closing edge: chained propagation
	// would dump the whole ln-gain error (0.2) on that single seam; least squares spreads it.
	drift := math.Exp(0.2)
	pairs := []PairFit{
		{A: 0, B: 1, Samples: 1000, Gain: 1},
		{A: 1, B: 2, Samples: 1000, Gain: 1},
		{A: 2, B: 3, Samples: 1000, Gain: 1},
		{A: 3, B: 0, Samples: 1000, Gain: drift},
	}
	gains, ok := solveGains(4, 0, pairs)
	require.True(t, ok)
	assert.InDelta(t, math.Exp(0.05), gains[1], 1e-9)
	assert.InDelta(t, math.Exp(0.10), gains[2], 1e-9)
	assert.InDelta(t, math.Exp(0.15), gains[3], 1e-9)
	// Per-edge residual is 0.05 everywhere instead of 0.2 concentrated on the closing edge.
	res := func(a, b int, g float64) float64 {
		return math.Abs(math.Log(gains[a]) - math.Log(gains[b]) - math.Log(g))
	}
	for _, pr := range pairs {
		assert.InDelta(t, 0.05, res(pr.A, pr.B, pr.Gain), 1e-9)
	}
}

func TestSolveOffsets_Chain(t *testing.T) {
	pairs := []PairFit{
		{A: 0, B: 1, Samples: 1000, Offset: 0.1},
		{A: 1, B: 2, Samples: 1000, Offset: -0.05},
	}
	offsets, ok := solveOffsets(3, 0, pairs, []float64{1, 1, 1})
	require.True(t, ok)
	assert.InDelta(t, 0.0, offsets[0], 1e-12)
	assert.InDelta(t, -0.1, offsets[1], 1e-9) // b_A − b_B = O ⇒ b1 = b0 − 0.1
	assert.InDelta(t, -0.05, offsets[2], 1e-9)
}

func TestSolveLinear(t *testing.T) {
	x, ok := solveLinear([][]float64{{2, 1}, {1, 3}}, []float64{5, 10})
	require.True(t, ok)
	assert.InDelta(t, 1.0, x[0], 1e-12)
	assert.InDelta(t, 3.0, x[1], 1e-12)

	_, ok = solveLinear([][]float64{{1, 2}, {2, 4}}, []float64{1, 2})
	assert.False(t, ok, "singular system must report failure")
}
