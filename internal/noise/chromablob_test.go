package noise

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// chromaCorr is the spatial autocorrelation of the CHROMA (R − mean) at the given lag, over the
// interior of the image. White per-pixel chroma noise gives ~0 at any lag > 0; noise reorganised
// into blobs of radius ≳ lag gives a large positive value.
func chromaCorr(im *fits.Image, lag int) float64 {
	c := make([]float64, im.W*im.H)
	for i := range c {
		m := (float64(im.Pix[0][i]) + float64(im.Pix[1][i]) + float64(im.Pix[2][i])) / 3
		c[i] = float64(im.Pix[0][i]) - m
	}
	var mean float64
	for _, v := range c {
		mean += v
	}
	mean /= float64(len(c))
	var num, den float64
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W-lag; x++ {
			a := c[y*im.W+x] - mean
			b := c[y*im.W+x+lag] - mean
			num += a * b
			den += a * a
		}
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// TestDenoise_TurnsChromaNoiseIntoColouredBlobs pins a real property of Denoise: it thresholds each
// plane INDEPENDENTLY, and its per-scale multipliers K={3,2.5,1.8,1,0} cut the fine scales hard
// while leaving the coarse ones alone. Flat chroma noise therefore comes out reorganised into soft,
// COLOURED ~10px structures rather than reduced — the fine grain the eye ignores replaced by blobs
// it cannot. Independent per-plane keep/discard decisions are what makes the residue chromatic.
//
// Scope, measured 2026-09-01: this is NOT the source of the coloured discs on the ASI2600MC run.
// noisefinish.go short-circuits this pass for an RGB channel whenever the joint GraXpert colour
// denoise is enabled, so a one-shot-colour master never reaches it. It DOES still run per filter on
// the mono LRGB path, where each channel master is denoised on its own and only then combined — the
// same independence, and therefore the same chromatic residue, one step later. Kept as the pin for
// that path and as the reason not to reuse this function channel-wise on colour data.
func TestDenoise_TurnsChromaNoiseIntoColouredBlobs(t *testing.T) {
	const w, h = 256, 256
	build := func() *fits.Image {
		im := fits.NewImage(w, h, 3)
		rng := rand.New(rand.NewSource(7))
		for i := 0; i < w*h; i++ {
			for c := 0; c < 3; c++ {
				im.Pix[c][i] = float32(0.10 + 0.01*rng.NormFloat64()) // flat sky + white noise
			}
		}
		return im
	}
	before := build()
	after := build()
	Denoise(after, DefaultOptions())

	lags := []int{1, 4, 8, 16}
	t.Log("lag | chroma autocorrelation before -> after")
	for _, lag := range lags {
		b, a := chromaCorr(before, lag), chromaCorr(after, lag)
		t.Logf(" %2d | %+.3f -> %+.3f", lag, b, a)
	}

	// The source noise is white: essentially no chroma correlation at any lag.
	assert.Less(t, math.Abs(chromaCorr(before, 8)), 0.05, "input chroma noise is white")
	// After denoising it is strongly correlated out to ~10px — that correlation IS the blob.
	assert.Greater(t, chromaCorr(after, 8), 0.30,
		"denoise leaves coarse-scale chroma structure: the coloured blobs")
}
