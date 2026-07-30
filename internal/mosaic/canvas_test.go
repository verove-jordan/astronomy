package mosaic

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// scenePanels renders one PanelImage per WCS with unit gain/zero offset over a plain sky.
func scenePanels(t *testing.T, wcss []fits.WCS, w, h int, sc testScene) []PanelImage {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	out := make([]PanelImage, 0, len(wcss))
	for i, wcs := range wcss {
		out = append(out, PanelImage{
			Label: labelN(i),
			Image: renderPanel(w, h, wcs, sc, 1, 0, 0, rng, 0),
			WCS:   wcs,
		})
	}
	return out
}

func labelN(i int) string {
	return []string{"p01", "p02", "p03", "p04", "p05", "p06", "p07", "p08", "p09"}[i]
}

func TestPlanCanvas_UnionScaleAndOrientation(t *testing.T) {
	const w, h = 64, 64
	const ov = 0.2
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	panels := scenePanels(t, gridWCS(t, 150, 20, 2, 2, w, h, ov), w, h, sc)

	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)

	expected := float64(w) + (1-ov)*float64(w) + 2*canvasPadPx
	assert.InDelta(t, expected, float64(canvas.W), 2, "canvas width")
	assert.InDelta(t, expected, float64(canvas.H), 2, "canvas height")
	// Tangent point = centroid of the four panel centers = the mosaic center.
	assert.InDelta(t, 150, canvas.WCS.RA0, 0.01)
	assert.InDelta(t, 20, canvas.WCS.Dec0, 0.01)
	// Anchor pixel scale.
	assert.InDelta(t, 2.0, canvas.WCS.ScaleArcsecPerPix(), 1e-9)
	// North-up/east-left: CD = [[-s,0],[0,s]] (det < 0 sky parity).
	assert.Negative(t, canvas.WCS.CD[0][0])
	assert.Positive(t, canvas.WCS.CD[1][1])
	assert.Zero(t, canvas.WCS.CD[0][1])
	assert.Zero(t, canvas.WCS.CD[1][0])
	// Every panel corner projects inside the canvas.
	for _, p := range panels {
		for _, pt := range [][2]float64{{0, 0}, {float64(w - 1), 0}, {0, float64(h - 1)}, {float64(w - 1), float64(h - 1)}} {
			ra, dec := p.WCS.PixToSky(pt[0], pt[1])
			x, y, ok := canvas.WCS.SkyToPix(ra, dec)
			require.True(t, ok)
			assert.GreaterOrEqual(t, x, 0.0)
			assert.GreaterOrEqual(t, y, 0.0)
			assert.Less(t, x, float64(canvas.W))
			assert.Less(t, y, float64(canvas.H))
		}
	}
}

func TestPlanCanvas_PlanCenterWins(t *testing.T) {
	const w, h = 64, 64
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	panels := scenePanels(t, gridWCS(t, 150, 20, 1, 1, w, h, 0.2), w, h, sc)

	canvas, err := PlanCanvas(panels, 150.02, 19.98, true)
	require.NoError(t, err)
	assert.Equal(t, 150.02, canvas.WCS.RA0)
	assert.Equal(t, 19.98, canvas.WCS.Dec0)
}

func TestPlanCanvas_Errors(t *testing.T) {
	const w, h = 64, 64
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	one := scenePanels(t, gridWCS(t, 150, 20, 1, 1, w, h, 0.2), w, h, sc)

	tests := []struct {
		name    string
		panels  []PanelImage
		ra, dec float64
		hasCtr  bool
		wantSub string
	}{
		{"no panels", nil, 0, 0, false, "no panels"},
		{"beyond the tangent horizon", one, 330, -20, true, "no panel projects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PlanCanvas(tt.panels, tt.ra, tt.dec, tt.hasCtr)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantSub)
		})
	}
}

func TestPlanCanvas_CapsWildSolve(t *testing.T) {
	// Two tiny panels 3° apart at 0.1"/px: the union would be ~108k px wide — must error, not OOM.
	const w, h = 32, 32
	fine := 0.1 / 3600
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	wcsA := tanWCS(t, w, h, 150, 20, fine, 0)
	wcsB := tanWCS(t, w, h, 150, 23, fine, 0)
	rng := rand.New(rand.NewSource(1))
	panels := []PanelImage{
		{Label: "p01", Image: renderPanel(w, h, wcsA, sc, 1, 0, 0, rng, 0), WCS: wcsA},
		{Label: "p02", Image: renderPanel(w, h, wcsB, sc, 1, 0, 0, rng, 0), WCS: wcsB},
	}
	_, err := PlanCanvas(panels, 0, 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sanity cap")
}

func TestPanelCanvasBBox_ClampsToCanvas(t *testing.T) {
	const w, h = 64, 64
	sc := testScene{ra0: 150, dec0: 20, base: 0.1}
	panels := scenePanels(t, gridWCS(t, 150, 20, 2, 1, w, h, 0.2), w, h, sc)
	canvas, err := PlanCanvas(panels, 0, 0, false)
	require.NoError(t, err)

	for _, p := range panels {
		x0, y0, x1, y1, ok := panelCanvasBBox(p, canvas)
		require.True(t, ok)
		assert.GreaterOrEqual(t, x0, 0)
		assert.GreaterOrEqual(t, y0, 0)
		assert.LessOrEqual(t, x1, canvas.W)
		assert.LessOrEqual(t, y1, canvas.H)
		assert.Greater(t, x1-x0, w-4, "bbox spans roughly the panel width")
		assert.Greater(t, y1-y0, h-4)
	}
	// A panel antipodal to the canvas tangent point cannot project: ok=false, no bbox.
	far, ok := fits.NewTanWCS(330, -20, 1, 1, [2][2]float64{{-testScale, 0}, {0, testScale}})
	require.True(t, ok)
	_, _, _, _, ok2 := panelCanvasBBox(PanelImage{Label: "px", Image: panels[0].Image, WCS: far}, canvas)
	assert.False(t, ok2)
}

func TestPlanCanvas_RejectsRGBPanel(t *testing.T) {
	rgb := fits.NewImage(16, 16, 3)
	wcs := tanWCS(t, 16, 16, 10, 10, testScale, 0)
	_, err := PlanCanvas([]PanelImage{{Label: "p01", Image: rgb, WCS: wcs}}, 0, 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mono")
}
