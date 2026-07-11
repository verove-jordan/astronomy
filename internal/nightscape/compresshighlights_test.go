package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestCompressHighlights_RGB_PreservesRatiosAndProtectsShadows(t *testing.T) {
	// pixel 0 is a bright coloured highlight (above the knee), pixel 1 is a dim sub-knee pixel.
	im := &fits.Image{W: 2, H: 1, C: 3, Pix: [][]float32{
		{0.9, 0.10}, // R
		{0.6, 0.08}, // G
		{0.3, 0.04}, // B
	}}
	CompressHighlights(im, 0.5, 0.8)

	// Bright pixel: all channels scaled by ONE factor, so 3:2:1 ratios survive exactly.
	if rg := float64(im.Pix[0][0] / im.Pix[1][0]); math.Abs(rg-1.5) > 1e-4 {
		t.Errorf("R/G ratio changed: got %.5f, want 1.5", rg)
	}
	if gb := float64(im.Pix[1][0] / im.Pix[2][0]); math.Abs(gb-2.0) > 1e-4 {
		t.Errorf("G/B ratio changed: got %.5f, want 2.0", gb)
	}
	if im.Pix[0][0] >= 0.9 {
		t.Errorf("bright R not rolled off: %.4f", im.Pix[0][0])
	}
	// Sub-knee pixel is identity (shadows/sky untouched).
	if im.Pix[0][1] != 0.10 || im.Pix[1][1] != 0.08 || im.Pix[2][1] != 0.04 {
		t.Errorf("sub-knee pixel modified: %.4f %.4f %.4f", im.Pix[0][1], im.Pix[1][1], im.Pix[2][1])
	}
}

func TestCompressHighlights_Mono_CapsBelowCeilKeepsOrder(t *testing.T) {
	im := &fits.Image{W: 3, H: 1, C: 1, Pix: [][]float32{{1.0, 0.9, 0.20}}}
	CompressHighlights(im, 0.6, 0.85)
	if im.Pix[0][0] >= 0.85 {
		t.Errorf("core not capped below ceil: %.4f", im.Pix[0][0])
	}
	if im.Pix[0][2] != 0.20 {
		t.Errorf("sub-knee pixel modified: %.4f", im.Pix[0][2])
	}
	if !(im.Pix[0][0] > im.Pix[0][1] && im.Pix[0][1] > im.Pix[0][2]) {
		t.Errorf("monotonic order broken: %v", im.Pix[0])
	}
}

func TestCompressHighlights_Disabled(t *testing.T) {
	tests := []struct {
		name       string
		knee, ceil float64
	}{
		{"knee zero", 0, 0.8},
		{"knee one", 1, 0.8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := &fits.Image{W: 1, H: 1, C: 1, Pix: [][]float32{{0.9}}}
			CompressHighlights(im, tt.knee, tt.ceil)
			if im.Pix[0][0] != 0.9 {
				t.Errorf("expected no-op, got %.4f", im.Pix[0][0])
			}
		})
	}
}
