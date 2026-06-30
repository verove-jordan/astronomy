package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// syntheticSky builds a grey linear sky: ~70 % background pixels with a small deterministic spread
// around bg, and ~30 % ramping up toward 1 (stars / Milky-Way core). A spread is essential — a single
// background spike would put the median exactly on the black point and stretch to 0.
func syntheticSky(w, h int, bg float64) *fits.Image {
	im := fits.NewImage(w, h, 3)
	n := w * h
	for i := 0; i < n; i++ {
		var v float64
		if i%10 < 7 {
			v = bg - 0.006 + 0.012*float64((i*7)%100)/100.0 // background, spread ±0.006
		} else {
			v = 0.02 + float64(i%1000)/1000.0*0.98 // ramp 0.02..1.0
		}
		for c := 0; c < 3; c++ {
			im.Pix[c][i] = float32(v)
		}
	}
	return im
}

// TestAutoStretch_HitsTargetBackground verifies the data-driven stretch lands the sky background on the
// requested target (the "automatic levels" the user asked for), across darker/balanced/brighter.
func TestAutoStretch_HitsTargetBackground(t *testing.T) {
	for _, target := range []float64{0.06, 0.09, 0.12} {
		im := syntheticSky(120, 120, 0.012)
		autoStretch(im, target, nil)
		lum := luminance(im)
		med := percentile(lum, 50)
		if math.Abs(float64(med)-target) > 0.035 {
			t.Fatalf("target %.2f: post-stretch background %.3f too far from target", target, med)
		}
	}
}

// TestAutoStretch_SkyMaskIgnoresForeground checks that statistics come from the sky region only: a dark
// foreground filling half the frame must not drag the stretch (the bug that blew the sky white).
func TestAutoStretch_SkyMaskIgnoresForeground(t *testing.T) {
	im := syntheticSky(120, 120, 0.012)
	mask := make([]float32, im.W*im.H)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			if y < im.H/2 { // top half = sky
				mask[y*im.W+x] = 1
			} else { // bottom half = dark foreground
				for c := 0; c < 3; c++ {
					im.Pix[c][y*im.W+x] = 0.0005
				}
			}
		}
	}
	autoStretch(im, 0.09, mask)
	// Median over the sky region only should sit near the target, not crushed by the dark foreground.
	sky := skyLuminance(im, mask, 300000)
	med := percentile(sky, 50)
	if math.Abs(float64(med)-0.09) > 0.04 {
		t.Fatalf("masked stretch background %.3f too far from 0.09", med)
	}
}

// TestSolveAsinhIntensity_ReachesTarget checks the solver lands the normalized background on the target
// ratio for a reachable case (bgNorm < target).
func TestSolveAsinhIntensity_ReachesTarget(t *testing.T) {
	bgNorm, target := 0.01, 0.1
	beta := solveAsinhIntensity(bgNorm, target)
	got := math.Asinh(bgNorm*beta) / math.Asinh(beta)
	if math.Abs(got-target) > 1e-3 {
		t.Fatalf("asinh ratio = %.4f, want %.4f (beta=%.3g)", got, target, beta)
	}
}
