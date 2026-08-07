package solar

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFrameSharpness is the contract lucky imaging rests on: the metric must rank frames by DETAIL,
// and must not be fooled by noise. Getting the second half wrong turns "keep the sharpest frames"
// into "keep the noisiest frames", and the stack then comes out flatter than its own inputs.
func TestFrameSharpness(t *testing.T) {
	sharpOf := func(psf, noise float64, seed uint64) float64 {
		s := defaultSun()
		s.psfSigma, s.noise, s.seed, s.proms, s.features = psf, noise, seed, 2, 14
		im := drawSun(s)
		l, ok := FitLimb(im)
		require.True(t, ok)
		return FrameSharpness(im, l)
	}

	t.Run("a sharper frame scores higher", func(t *testing.T) {
		var prev float64
		for i, psf := range []float64{4.0, 3.0, 2.0, 1.2} {
			got := sharpOf(psf, 0.004, 1)
			t.Logf("psf sigma %.1f → %.5f", psf, got)
			if i > 0 {
				assert.Greater(t, got, prev, "sharper must score higher")
			}
			prev = got
		}
	})

	t.Run("noise does not raise the score", func(t *testing.T) {
		// The failure mode this metric exists to prevent. A plain gradient energy rises steeply with
		// noise; a noise-corrected one must not.
		clean := sharpOf(2.0, 0.002, 3)
		noisy := sharpOf(2.0, 0.030, 3)
		t.Logf("same sharpness, noise 0.002 → %.5f, noise 0.030 → %.5f", clean, noisy)
		assert.Less(t, noisy, clean*1.25, "a noisier frame of equal sharpness must not score higher")
	})

	t.Run("a sharp noisy frame still beats a soft clean one", func(t *testing.T) {
		sharpNoisy := sharpOf(1.2, 0.020, 5)
		softClean := sharpOf(4.0, 0.002, 5)
		t.Logf("sharp+noisy %.5f vs soft+clean %.5f", sharpNoisy, softClean)
		assert.Greater(t, sharpNoisy, softClean, "detail must outrank cleanliness")
	})

	t.Run("pure noise scores near zero", func(t *testing.T) {
		s := defaultSun()
		s.psfSigma, s.noise, s.u1, s.u2, s.proms = 1, 0.05, 0, 0, 0
		im := drawSun(s) // a featureless disc plus heavy noise: no detail to find
		l, ok := FitLimb(im)
		require.True(t, ok)
		got := FrameSharpness(im, l)
		t.Logf("featureless + heavy noise → %.5f", got)
		assert.Less(t, got, 0.02, "no detail means no score, whatever the noise")
	})

	t.Run("exposure does not change the ranking", func(t *testing.T) {
		// A bracketed session must not have its frame ranking decided by brightness.
		s := defaultSun()
		s.psfSigma, s.noise, s.proms, s.features = 2, 0.004, 2, 14
		im := drawSun(s)
		l, ok := FitLimb(im)
		require.True(t, ok)
		base := FrameSharpness(im, l)

		bright := drawSun(s)
		for i := range bright.Pix[0] {
			bright.Pix[0][i] *= 3
		}
		bl, ok := FitLimb(bright)
		require.True(t, ok)
		assert.InDelta(t, base, FrameSharpness(bright, bl), base*0.15,
			"a 3x brighter copy of the same frame must score the same")
	})
}
