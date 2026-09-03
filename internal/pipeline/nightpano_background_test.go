package pipeline

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// The background pass is soft-fail by contract: every way it can decline has to leave the canvas
// byte-identical and report false, so the caller falls back to the polynomial rather than shipping a
// half-corrected sky.
func TestGraxpertCanvasBackground_SoftFailsWithoutTouchingTheCanvas(t *testing.T) {
	const w, h = 32, 32
	newCanvas := func() (*fits.Image, []float32) {
		im := fits.NewImage(w, h, 3)
		cov := make([]float32, w*h)
		for i := range cov {
			cov[i] = 1
			for c := 0; c < 3; c++ {
				im.Pix[c][i] = float32(i%17) / 100
			}
		}
		return im, cov
	}

	tests := []struct {
		name string
		mut  func(im *fits.Image, cov []float32) (*fits.Image, []float32, Options)
	}{
		{"no runner configured", func(im *fits.Image, cov []float32) (*fits.Image, []float32, Options) {
			return im, cov, Options{}
		}},
		{"coverage does not match the canvas", func(im *fits.Image, cov []float32) (*fits.Image, []float32, Options) {
			return im, cov[:10], Options{}
		}},
		{"nothing is covered", func(im *fits.Image, cov []float32) (*fits.Image, []float32, Options) {
			for i := range cov {
				cov[i] = 0
			}
			return im, cov, Options{}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im, cov := newCanvas()
			want := append([]float32(nil), im.Pix[0]...)
			im, cov, opts := tt.mut(im, cov)

			ok := graxpertCanvasBackground(context.Background(), opts, &Result{}, im, cov, t.TempDir(), "test")

			assert.False(t, ok, "declined passes must report false")
			assert.Equal(t, want, im.Pix[0], "the canvas must be left untouched when the pass declines")
		})
	}
}

func TestBoxBlurPlane(t *testing.T) {
	const w, h = 16, 16

	t.Run("a constant plane is unchanged", func(t *testing.T) {
		p := make([]float32, w*h)
		for i := range p {
			p[i] = 0.25
		}
		got := boxBlurPlane(p, w, h, 3)
		for i, v := range got {
			require.InDelta(t, 0.25, v, 1e-6, "pixel %d", i)
		}
	})

	t.Run("radius zero copies rather than aliases", func(t *testing.T) {
		p := []float32{1, 2, 3, 4}
		got := boxBlurPlane(p, 2, 2, 0)
		assert.Equal(t, p, got)
		got[0] = 99
		assert.Equal(t, float32(1), p[0], "the result must not alias the input")
	})

	// The pass exists to strip pixel-scale residue from a surface that is smooth by construction,
	// so the thing to protect is that a lone spike is flattened while the total is conserved.
	t.Run("a spike is spread without changing the mean", func(t *testing.T) {
		p := make([]float32, w*h)
		p[8*w+8] = 1
		var before float64
		for _, v := range p {
			before += float64(v)
		}

		got := boxBlurPlane(p, w, h, 2)

		var after, peak float64
		for _, v := range got {
			after += float64(v)
			peak = math.Max(peak, float64(v))
		}
		assert.Less(t, peak, 0.1, "the spike survived the blur")
		assert.InDelta(t, before, after, 1e-4, "the blur changed the total signal")
	})
}
