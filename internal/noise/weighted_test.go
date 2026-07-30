package noise

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noisyPlane builds a deterministic sky+noise plane for the weighted-denoise tests.
func noisyPlane(w, h int, seed uint32) []float32 {
	p := newPlane(w, h, 0.1)
	addNoise(p, newRNG(seed), 0.01)
	return p
}

// planeSigma measures the robust noise of a sub-rectangle via the package's own tile estimator on a
// copy (Measure works on whole images; here a plain std-dev is enough for a pure-noise fixture).
func planeSigma(p []float32, w int, x0, x1, h int) float64 {
	var sum, sum2 float64
	n := 0
	for y := 0; y < h; y++ {
		for x := x0; x < x1; x++ {
			v := float64(p[y*w+x])
			sum += v
			sum2 += v * v
			n++
		}
	}
	mean := sum / float64(n)
	return sum2/float64(n) - mean*mean // variance (comparisons only need monotonicity)
}

func TestDenoiseWeighted_NilMatchesDenoise(t *testing.T) {
	const w, h = 256, 256
	a := monoImage(w, h, noisyPlane(w, h, 7))
	b := monoImage(w, h, noisyPlane(w, h, 7))

	Denoise(a, DefaultOptions())
	require.NoError(t, DenoiseWeighted(b, DefaultOptions(), nil))
	assert.Equal(t, a.Pix[0], b.Pix[0], "nil weight must be byte-identical to Denoise")
}

func TestDenoiseWeighted_ZeroWeightIsByteIdentical(t *testing.T) {
	const w, h = 128, 128
	orig := noisyPlane(w, h, 11)
	im := monoImage(w, h, append([]float32(nil), orig...))

	require.NoError(t, DenoiseWeighted(im, DefaultOptions(), make([]float32, w*h)))
	assert.Equal(t, orig, im.Pix[0], "weight 0 everywhere must not touch a single byte")
}

func TestDenoiseWeighted_HalfPlaneReducesSigmaOnlyThere(t *testing.T) {
	const w, h = 256, 256
	orig := noisyPlane(w, h, 23)
	im := monoImage(w, h, append([]float32(nil), orig...))
	weight := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := w / 2; x < w; x++ {
			weight[y*w+x] = 1.5
		}
	}

	require.NoError(t, DenoiseWeighted(im, DefaultOptions(), weight))

	for y := 0; y < h; y++ {
		left := im.Pix[0][y*w : y*w+w/2]
		wantLeft := orig[y*w : y*w+w/2]
		require.Equal(t, wantLeft, left, "zero-weight half must stay byte-identical (row %d)", y)
	}
	before := planeSigma(orig, w, w/2+8, w-8, h)
	after := planeSigma(im.Pix[0], w, w/2+8, w-8, h)
	assert.Less(t, after, before*0.8, "weighted half must be measurably denoised")
}

func TestDenoiseWeighted_LengthMismatchErrors(t *testing.T) {
	im := monoImage(64, 64, noisyPlane(64, 64, 3))
	err := DenoiseWeighted(im, DefaultOptions(), make([]float32, 7))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "weight plane")
}
