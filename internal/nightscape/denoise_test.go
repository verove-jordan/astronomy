package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestChromaBlur_SmoothsChromaKeepsLuma checks chromaBlur melts colour speckle while preserving
// luminance (so stars and structure stay sharp).
func TestChromaBlur_SmoothsChromaKeepsLuma(t *testing.T) {
	w, h := 16, 16
	im := fits.NewImage(w, h, 3)
	for i := range im.Pix[0] {
		im.Pix[0][i], im.Pix[1][i], im.Pix[2][i] = 0.3, 0.3, 0.3
		if i%2 == 0 {
			im.Pix[0][i] += 0.1 // red speckle
		} else {
			im.Pix[2][i] += 0.1 // blue speckle
		}
	}
	const star = 8*16 + 8
	im.Pix[0][star], im.Pix[1][star], im.Pix[2][star] = 0.9, 0.9, 0.9 // a neutral bright star

	lumBefore := luminance(im)
	chromaBefore := math.Abs(float64(im.Pix[0][40] - lumBefore[40])) // red-speckle pixel's chroma magnitude

	chromaBlur(im, 3)

	lumAfter := luminance(im)
	if d := math.Abs(float64(lumAfter[star] - lumBefore[star])); d > 1e-3 {
		t.Fatalf("star luminance changed by %.4g (should be preserved)", d)
	}
	chromaAfter := math.Abs(float64(im.Pix[0][40] - lumAfter[40]))
	if chromaAfter >= chromaBefore*0.6 {
		t.Fatalf("chroma speckle not reduced: before %.4f after %.4f", chromaBefore, chromaAfter)
	}
}

func TestChromaBlur_MonoNoop(t *testing.T) {
	im := fits.NewImage(4, 4, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = float32(i)
	}
	chromaBlur(im, 3)
	for i := range im.Pix[0] {
		if im.Pix[0][i] != float32(i) {
			t.Fatalf("mono image altered at %d", i)
		}
	}
}
