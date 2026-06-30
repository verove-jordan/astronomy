package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestRemoveSkyGradient_FlattensGradientKeepsBand checks the mask-aware flatten removes a large-scale
// horizontal colour gradient over the sky region while preserving a compact bright "band" (high
// frequency) and ignoring the masked-out foreground.
func TestRemoveSkyGradient_FlattensGradientKeepsBand(t *testing.T) {
	w, h := 96, 96
	im := fits.NewImage(w, h, 3)
	mask := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if y < 72 { // top 75 % sky, with a left→right (reddening) light-pollution gradient
				mask[i] = 1
				g := float32(x) / float32(w)
				im.Pix[0][i] = 0.05 + 0.10*g
				im.Pix[1][i] = 0.04 + 0.05*g
				im.Pix[2][i] = 0.04 + 0.02*g
			}
			// else foreground (mask 0, pixels left at 0)
		}
	}
	// A compact bright band/star in the sky (high-frequency structure that must survive).
	for y := 28; y < 34; y++ {
		for x := 44; x < 50; x++ {
			for c := 0; c < 3; c++ {
				im.Pix[c][y*w+x] = 0.9
			}
		}
	}
	band0 := im.Pix[0][31*w+47]
	grad0 := math.Abs(float64(im.Pix[0][10*w+90] - im.Pix[0][10*w+5]))

	removeSkyGradient(im, mask, 16, 1.0)

	grad1 := math.Abs(float64(im.Pix[0][10*w+90] - im.Pix[0][10*w+5]))
	if grad1 >= grad0*0.5 {
		t.Fatalf("gradient not flattened: before %.4f after %.4f", grad0, grad1)
	}
	if band1 := im.Pix[0][31*w+47]; float64(band1) < 0.5*float64(band0) {
		t.Fatalf("bright band eaten by the flatten: before %.3f after %.3f", band0, band1)
	}
}

// TestRemoveSkyGradient_NilMaskNoop checks a mismatched/nil mask is a safe no-op.
func TestRemoveSkyGradient_NilMaskNoop(t *testing.T) {
	im := fits.NewImage(8, 8, 3)
	for c := 0; c < 3; c++ {
		for i := range im.Pix[c] {
			im.Pix[c][i] = 0.3
		}
	}
	removeSkyGradient(im, nil, 16, 1.0)
	if im.Pix[0][0] != 0.3 {
		t.Fatalf("nil mask should be a no-op, got %v", im.Pix[0][0])
	}
}

// TestCompressHighlights_Ceiling checks a maxed (white) core is rolled below the ceiling, not left white —
// both the default ceiling (ceil ≤ 0 → highlightCeiling) and an explicit per-look ceiling.
func TestCompressHighlights_Ceiling(t *testing.T) {
	mk := func() *fits.Image {
		im := fits.NewImage(1, 1, 3)
		im.Pix[0][0], im.Pix[1][0], im.Pix[2][0] = 1.0, 1.0, 1.0
		return im
	}
	lumOf := func(im *fits.Image) float64 {
		return float64(0.299*im.Pix[0][0] + 0.587*im.Pix[1][0] + 0.114*im.Pix[2][0])
	}
	// ceil ≤ 0 → the default highlightCeiling const.
	im := mk()
	compressHighlights(im, 0.35, 0)
	if l := lumOf(im); l > highlightCeiling+1e-3 {
		t.Fatalf("core luminance %.3f exceeds default ceiling %.3f", l, highlightCeiling)
	} else if l < 0.5 {
		t.Fatalf("core over-compressed to %.3f (should stay a bright glow)", l)
	}
	// an explicit lower ceiling is respected → a dimmer core (the natural look uses 0.42).
	im = mk()
	compressHighlights(im, 0.28, 0.62)
	if l := lumOf(im); l > 0.62+1e-3 {
		t.Fatalf("core luminance %.3f exceeds explicit ceiling 0.62", l)
	}
}

// TestGridFillUnknown_ExtrapolatesFromNeighbors checks the v5 normalized-convolution fill carries a
// gradient into the unknown (sparse-sky) cells from their reliable neighbours instead of dropping them to
// the floor — the mechanism that flattens the "orange bottom" behind the trees.
func TestGridFillUnknown_ExtrapolatesFromNeighbors(t *testing.T) {
	gw, gh := 5, 5
	grid := make([]float32, gw*gh)
	known := make([]float32, gw*gh)
	for y := 0; y < 3; y++ { // top 3 rows known: a downward-increasing gradient 1,2,3
		for x := 0; x < gw; x++ {
			grid[y*gw+x] = float32(y + 1)
			known[y*gw+x] = 1
		}
	}
	// bottom 2 rows unknown (known=0, grid=0). With a 0 floor, a global-snap would leave them ~0.
	gridFillUnknown(grid, known, gw, gh, 0.0)
	if v := grid[3*gw+2]; v < 2.0 {
		t.Fatalf("unknown cell not extrapolated from its bright neighbour: %.3f (want ≳3)", v)
	}
	if v := grid[4*gw+2]; v < 1.5 {
		t.Fatalf("deep unknown cell fell toward the floor instead of extrapolating: %.3f", v)
	}
}

// TestRemoveSkyGradient_FlattensSparseBottom is the integration case: a warm vertical light-pollution
// gradient whose bottom strip is SPARSE sky (mostly masked foliage, too few pixels per cell to measure) —
// the "orange bottom". The v5 extrapolated grid must still flatten it; with the old global-snap those
// cells stayed bright.
func TestRemoveSkyGradient_FlattensSparseBottom(t *testing.T) {
	w, h := 96, 96
	im := fits.NewImage(w, h, 3)
	mask := make([]float32, w*h)
	set := func(i int, v float32) { im.Pix[0][i], im.Pix[1][i], im.Pix[2][i] = v, v*0.7, v*0.5 } // warm
	val := func(y int) float32 { return 0.05 + 0.30*float32(y)/float32(h) }                      // top dark → bottom bright
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			switch {
			case y < 80: // dense sky carrying the vertical gradient
				mask[i] = 1
				set(i, val(y))
			case x%24 == 0 && y%4 == 0: // sparse horizon sky between "trees": <6 px/cell → unknown cells
				mask[i] = 1
				set(i, val(y))
			}
		}
	}
	topBefore := im.Pix[0][4*w+10] // dense dark top
	botBefore := im.Pix[0][88*w+0] // a sparse bright bottom sky pixel
	if botBefore <= topBefore {
		t.Fatalf("setup: bottom %.3f should start brighter than top %.3f", botBefore, topBefore)
	}
	gradBefore := float64(botBefore - topBefore)

	removeSkyGradient(im, mask, 16, gradStrength)

	gradAfter := math.Abs(float64(im.Pix[0][88*w+0] - im.Pix[0][4*w+10]))
	if gradAfter >= 0.6*gradBefore {
		t.Fatalf("sparse-bottom gradient not flattened: before %.4f after %.4f (want < %.4f)", gradBefore, gradAfter, 0.6*gradBefore)
	}
}

// TestMedian3_RejectsOutlier checks the grid median replaces a lone bright cell with its neighbourhood.
func TestMedian3_RejectsOutlier(t *testing.T) {
	gw, gh := 3, 3
	grid := []float32{1, 1, 1, 1, 9, 1, 1, 1, 1} // centre is a bright-structure outlier
	out := median3(grid, gw, gh)
	if out[4] != 1 {
		t.Fatalf("median3 did not reject the outlier centre: %v", out[4])
	}
}
