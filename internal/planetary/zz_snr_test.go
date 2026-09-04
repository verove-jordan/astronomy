package planetary

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZAsiSNR measures the per-frame signal-to-noise of the ASI capture, to answer "why is it noisy"
// with a number instead of an opinion.
func TestZZAsiSNR(t *testing.T) {
	paths, _ := filepath.Glob("/Users/jordanverove/projects/perso/astronomy/input/moon/L/*.fit")
	if len(paths) == 0 {
		t.Skip("no ASI frames")
	}
	sort.Strings(paths)
	im, err := fits.ReadImage(paths[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// FITS values here are the raw 16-bit ADU scaled to 0..1 by the reader; put them back in ADU.
	const full = 65535.0 // fits.ReadImage already returns ADU for a 16-bit frame
	vals := make([]float64, 0, len(im.Pix[0]))
	for _, v := range im.Pix[0] {
		vals = append(vals, float64(v))
	}
	sv := append([]float64(nil), vals...)
	sort.Float64s(sv)
	sky := sv[len(sv)/2]
	peak := sv[int(float64(len(sv))*0.9995)]

	// Noise: standard deviation inside a small patch of LIT surface, where the true signal is
	// smooth, so the scatter is the frame's own noise.
	cx, cy, r, ok := trackerDisc(im)
	if !ok {
		t.Skip("no disc")
	}
	var patch []float64
	px, py := int(cx), int(cy-r*0.4) // well inside the lit half
	for y := py - 24; y <= py+24; y++ {
		for x := px - 24; x <= px+24; x++ {
			if x < 0 || y < 0 || x >= im.W || y >= im.H {
				continue
			}
			patch = append(patch, float64(im.Pix[0][y*im.W+x]))
		}
	}
	mean := 0.0
	for _, v := range patch {
		mean += v
	}
	mean /= float64(len(patch))
	varr := 0.0
	for _, v := range patch {
		varr += (v - mean) * (v - mean)
	}
	sigma := math.Sqrt(varr / float64(len(patch)))

	signal := mean - sky
	t.Logf("frames: %d", len(paths))
	t.Logf("sky/bias level : %.0f ADU", sky)
	t.Logf("lit surface    : %.0f ADU  (signal above bias %.0f ADU = %.2f%% of full scale)",
		mean, signal, 100*signal/full)
	t.Logf("peak           : %.0f ADU", peak)
	t.Logf("noise sigma    : %.1f ADU", sigma)
	t.Logf("SNR per frame  : %.1f", signal/sigma)
	t.Logf("SNR after stacking %d frames: %.1f", len(paths), signal/sigma*math.Sqrt(float64(len(paths))))
	_ = os.Stdout
}
