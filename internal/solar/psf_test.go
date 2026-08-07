package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// syntheticDisc renders a limb-darkened disc blurred by a known Gaussian, optionally with the bright
// shelf a sharpener leaves just inside the edge.
//
// The blur is applied as an analytic edge profile rather than by convolving a hard disc, so the test
// knows the answer exactly instead of knowing it to within the accuracy of its own blur.
func syntheticDisc(side int, radius, sigma, overshoot float64) (*fits.Image, Limb) {
	im := fits.NewImage(side, side, 1)
	c := float64(side-1) / 2
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			d := math.Hypot(float64(x)-c, float64(y)-c)
			// The limb as an error function of the distance from it: the exact result of a Gaussian
			// PSF acting on a step, which is what the measurement claims to invert.
			edge := 0.5 * math.Erfc((d-radius)/(sigma*math.Sqrt2))
			// Limb darkening: a smooth fall from centre to limb, the thing the measurement must not
			// mistake for the edge itself.
			ld := 1 - 0.35*math.Min(d/radius, 1)*math.Min(d/radius, 1)
			v := ld * edge
			if overshoot > 0 && d < radius {
				// A sharpener's shelf: a bump hugging the inside of the limb.
				v *= 1 + overshoot*math.Exp(-math.Pow((d-(radius-2*sigma))/(2*sigma), 2))
			}
			im.Pix[0][y*side+x] = float32(v)
		}
	}
	return im, Limb{CX: c, CY: c, R: radius}
}

func TestMeasurePSF_RecoversTheBlurItWasGiven(t *testing.T) {
	tests := []struct {
		name  string
		sigma float64
	}{
		{"sharp", 0.8},
		{"typical phone clip", 1.5},
		{"soft", 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im, l := syntheticDisc(400, 150, tt.sigma, 0)

			psf := MeasurePSF(im, l)

			require.True(t, psf.OK)
			assert.InDelta(t, tt.sigma, psf.SigmaPx, 0.2, "the measured width must be the width applied")
			assert.False(t, psf.Sharpened(), "an unsharpened edge must not read as sharpened")
		})
	}
}

// The whole point of measuring on the derivative: limb darkening must not widen the answer.
func TestMeasurePSF_IgnoresLimbDarkening(t *testing.T) {
	im, l := syntheticDisc(400, 150, 1.2, 0)

	psf := MeasurePSF(im, l)

	require.True(t, psf.OK)
	assert.InDelta(t, 1.2, psf.SigmaPx, 0.2,
		"a limb-darkened disc must measure the same width as a flat one — reading the edge at a "+
			"fraction of the disc brightness instead is what makes it come out several times too wide")
}

// The property the per-sector estimator exists for. The fitted circle is never exactly the limb — on
// a real 900 px master it misses by ~2.5 px RMS — and an estimator that averages the whole ring first
// reports that miss as blur. Measured on real masters, that made the same image come back anywhere
// between 1.3 and 4.9 px depending on which fitted circle it was handed.
func TestMeasurePSF_IsNotFooledByAnImperfectLimbFit(t *testing.T) {
	im, l := syntheticDisc(500, 180, 1.5, 0)
	truth := MeasurePSF(im, l)
	require.True(t, truth.OK)

	tests := []struct {
		name string
		limb Limb
	}{
		{"centre off by 2 px", Limb{CX: l.CX + 2, CY: l.CY, R: l.R}},
		{"centre off diagonally", Limb{CX: l.CX - 1.5, CY: l.CY + 1.5, R: l.R}},
		{"radius over by 2 px", Limb{CX: l.CX, CY: l.CY, R: l.R + 2}},
		{"radius under by 2 px", Limb{CX: l.CX, CY: l.CY, R: l.R - 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MeasurePSF(im, tt.limb)

			require.True(t, got.OK)
			assert.InDelta(t, truth.SigmaPx, got.SigmaPx, 0.35,
				"a wrong circle shifts each wedge's edge; it must not widen it")
		})
	}
}

func TestMeasurePSF_DetectsAPreSharpenedEdge(t *testing.T) {
	plain, l := syntheticDisc(400, 150, 1.4, 0)
	sharpened, _ := syntheticDisc(400, 150, 1.4, 0.05)

	assert.False(t, MeasurePSF(plain, l).Sharpened())
	got := MeasurePSF(sharpened, l)
	require.True(t, got.OK)
	assert.True(t, got.Sharpened(), "a bright shelf inside the limb is a sharpener's signature")
	assert.Greater(t, got.Overshoot, 0.01)
}

func TestMeasurePSF_RefusesWhatItCannotMeasure(t *testing.T) {
	im, l := syntheticDisc(400, 150, 1.2, 0)
	tests := []struct {
		name string
		im   *fits.Image
		limb Limb
	}{
		{"no image", nil, l},
		{"no radius", im, Limb{CX: l.CX, CY: l.CY}},
		{"flat field", fits.NewImage(400, 400, 1), l},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, MeasurePSF(tt.im, tt.limb).OK, "an unmeasurable frame must say so, not guess")
		})
	}
}

func TestResolveFinish_DeconvolvesAtTheMeasuredWidth(t *testing.T) {
	im, l := syntheticDisc(400, 150, 1.1, 0)
	in := DefaultFinish()
	in.DeconvSigma = 2.0

	out, psf, notes := ResolveFinish(im, l, in)

	require.True(t, psf.OK)
	assert.InDelta(t, psf.SigmaPx, out.DeconvSigma, 1e-9, "the resolved width is the measured one")
	assert.NotEqual(t, in.DeconvSigma, out.DeconvSigma)
	assert.Equal(t, in.DeconvIters, out.DeconvIters, "an unsharpened source keeps its iterations")
	assert.NotEmpty(t, notes, "a run must say what it changed")
}

func TestResolveFinish_BacksOffWhenTheCameraAlreadySharpened(t *testing.T) {
	im, l := syntheticDisc(400, 150, 1.4, 0.05)
	in := DefaultFinish()

	out, psf, _ := ResolveFinish(im, l, in)

	require.True(t, psf.Sharpened())
	assert.Less(t, out.DeconvIters, in.DeconvIters,
		"deconvolving to convergence on top of the camera's own sharpening double-counts")
	assert.Greater(t, out.DeconvIters, 0, "the camera's local-contrast trick does not undo the real blur")
}

func TestResolveFinish_LeavesAnExplicitWidthAlone(t *testing.T) {
	im, l := syntheticDisc(400, 150, 1.1, 0)
	in := DefaultFinish()
	in.DeconvAuto = false
	in.DeconvSigma = 2.0

	out, psf, notes := ResolveFinish(im, l, in)

	assert.Equal(t, in, out, "naming a width must turn the measurement off entirely")
	assert.False(t, psf.OK)
	assert.Empty(t, notes)
}

// The caller's preset is reused for every window of the time-lapse and every supervised candidate,
// so resolving one render must not rewrite the settings the next one starts from.
func TestResolveFinish_DoesNotMutateTheCallersGains(t *testing.T) {
	im, l := syntheticDisc(400, 150, 2.4, 0)
	in := DefaultFinish()
	before := append([]float64(nil), in.Sharpen.Gains...)

	out, _, _ := ResolveFinish(im, l, in)

	assert.Equal(t, before, in.Sharpen.Gains, "the input options must come back untouched")
	assert.NotSame(t, &in.Sharpen.Gains[0], &out.Sharpen.Gains[0])
}
