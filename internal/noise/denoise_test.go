package noise

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// denoiseScene builds a 256² deep-sky-like plane: flat background, four bright (protected) stars,
// one extended low-SNR blob, plus white noise. Returns the star centers and the blob window.
func denoiseScene(rng *xorshift32) (w, h int, p []float32, stars [][2]int, bcx, bcy, bhalf int) {
	w, h = 256, 256
	p = newPlane(w, h, 0.05)
	stars = [][2]int{{40, 40}, {216, 40}, {40, 216}, {216, 216}}
	for _, s := range stars {
		addGaussian(p, w, h, s[0], s[1], 0.12, 1.6) // peak SNR ~60: high-SNR, protected
	}
	bcx, bcy, bhalf = 128, 128, 42
	addGaussian(p, w, h, bcx, bcy, 0.009, 14) // extended, peak SNR ~4.5
	addNoise(p, rng, 2e-3)
	return
}

func windowFlux(p []float32, w, cx, cy, half int, bg float64) float64 {
	var s float64
	for y := cy - half; y <= cy+half; y++ {
		for x := cx - half; x <= cx+half; x++ {
			s += float64(p[y*w+x]) - bg
		}
	}
	return s
}

func TestDenoise_ReducesNoise(t *testing.T) {
	rng := newRNG(11)
	w, h, p, _, _, _, _ := denoiseScene(rng)
	before := Measure(monoImage(w, h, p)).Sigma
	require.Positive(t, before)

	im := monoImage(w, h, append([]float32(nil), p...))
	Denoise(im, DefaultOptions())
	after := Measure(im).Sigma
	assert.Lessf(t, after, 0.35*before, "sigma before %g after %g", before, after)
}

func TestDenoise_PreservesStarPeaks(t *testing.T) {
	rng := newRNG(12)
	w, h, p, stars, _, _, _ := denoiseScene(rng)
	orig := append([]float32(nil), p...)

	im := monoImage(w, h, p) // shares p; Denoise mutates in place
	Denoise(im, DefaultOptions())
	for _, s := range stars {
		i := s[1]*w + s[0]
		assert.InEpsilonf(t, float64(orig[i]), float64(im.Pix[0][i]), 0.01, "star peak at %v", s)
	}
}

func TestDenoise_PreservesBlobFlux(t *testing.T) {
	rng := newRNG(13)
	w, h, p, _, cx, cy, half := denoiseScene(rng)
	orig := append([]float32(nil), p...)
	before := windowFlux(orig, w, cx, cy, half, 0.05)

	im := monoImage(w, h, p)
	Denoise(im, DefaultOptions())
	after := windowFlux(im.Pix[0], w, cx, cy, half, 0.05)
	assert.InEpsilonf(t, before, after, 0.10, "blob flux %g -> %g", before, after)
}

func TestDenoise_StrengthMonotonic(t *testing.T) {
	rng := newRNG(14)
	w, h, p, _, _, _, _ := denoiseScene(rng)
	strengths := []float64{0.25, 0.6, 1.0}
	resids := make([]float64, len(strengths))
	for i, s := range strengths {
		im := monoImage(w, h, append([]float32(nil), p...))
		o := DefaultOptions()
		o.Strength = s
		Denoise(im, o)
		resids[i] = Measure(im).Sigma
	}
	for i := 1; i < len(resids); i++ {
		assert.LessOrEqualf(t, resids[i], resids[i-1]+1e-9, "resid %v not monotonic", resids)
	}
	assert.Lessf(t, resids[len(resids)-1], resids[0], "stronger denoise must leave less residual: %v", resids)
}

func TestDenoise_StepEdgeNoRinging(t *testing.T) {
	const w, h = 256, 384
	const a, b, sigma = 0.1, 0.4, 2e-3
	rng := newRNG(15)
	p := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := a
			if x >= w/2 {
				v = b
			}
			p[y*w+x] = float32(v + rng.gauss()*sigma)
		}
	}
	im := monoImage(w, h, p)
	Denoise(im, DefaultOptions())

	// Row-average to isolate ringing from per-pixel noise (which averages down to sigma/sqrt(h)).
	var overshoot float64
	for x := 0; x < w; x++ {
		var s float64
		for y := 0; y < h; y++ {
			s += float64(im.Pix[0][y*w+x])
		}
		v := s / float64(h)
		overshoot = math.Max(overshoot, math.Max(v-b, a-v))
	}
	assert.Lessf(t, overshoot, 0.5*sigma, "step-edge overshoot %g", overshoot)
}

func TestDenoise_ZeroStrengthNoOp(t *testing.T) {
	rng := newRNG(16)
	w, h, p, _, _, _, _ := denoiseScene(rng)
	orig := append([]float32(nil), p...)
	im := monoImage(w, h, p)
	o := DefaultOptions()
	o.Strength = 0
	Denoise(im, o)
	assert.Equal(t, orig, im.Pix[0])
}

func TestDenoise_SoftFailOnNonFinite(t *testing.T) {
	const w, h = 64, 64
	rng := newRNG(17)
	p := newPlane(w, h, 0.05)
	addNoise(p, rng, 2e-3)
	p[100] = float32(math.NaN())
	orig := append([]float32(nil), p...)

	im := monoImage(w, h, p)
	Denoise(im, DefaultOptions())
	for i := range orig {
		if i == 100 {
			require.True(t, math.IsNaN(float64(im.Pix[0][i]))) // untouched NaN
			continue
		}
		require.Equalf(t, orig[i], im.Pix[0][i], "plane changed at %d despite soft-fail", i)
	}
}

func TestDenoise_MultiChannel(t *testing.T) {
	const w, h = 128, 128
	rng := newRNG(18)
	im := &fits.Image{W: w, H: h, C: 3, Pix: make([][]float32, 3)}
	before := make([]float64, 3)
	for c := 0; c < 3; c++ {
		pl := newPlane(w, h, 0.05)
		addNoise(pl, rng, 2e-3)
		im.Pix[c] = pl
		before[c] = Measure(monoImage(w, h, append([]float32(nil), pl...))).Sigma
	}
	Denoise(im, DefaultOptions())
	for c := 0; c < 3; c++ {
		after := Measure(monoImage(w, h, im.Pix[c])).Sigma
		assert.Lessf(t, after, before[c], "channel %d not denoised", c)
	}
}
