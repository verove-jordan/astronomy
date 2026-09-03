package calib

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// truth is a plausible lens falloff: 1 at the centre, about 0.48 at the corner.
func truth(r float64) float64 {
	r2 := r * r
	return 1 - 0.35*r2 - 0.10*r2*r2 - 0.07*r2*r2*r2
}

func profileOf(f func(float64) float64, bins int) *RadialProfile {
	out := &RadialProfile{Level: make([]float64, bins), MeanR: make([]float64, bins)}
	for i := range out.Level {
		r := (float64(i) + 0.5) / float64(bins)
		out.MeanR[i], out.Level[i] = r, f(r)
	}
	return out
}

func TestFitRadialVignette_RecoversTheFalloff(t *testing.T) {
	v, err := FitRadialVignette(profileOf(truth, 20), 0.45)
	require.NoError(t, err)
	assert.Less(t, v.RMS, 1e-6, "an even polynomial must fit an even polynomial exactly")
	for _, r := range []float64{0, 0.2, 0.5, 0.8, 1.0} {
		assert.InDelta(t, truth(r), v.At(r), 1e-6, "at r=%.1f", r)
	}
}

// The point of the whole exercise: the centre of a phone flat carries a reflection of the phone, and
// fitting through it would bake that reflection into the model as a central brightening. Fitting only
// outside it must reconstruct the true centre anyway.
func TestFitRadialVignette_IgnoresAContaminatedCentre(t *testing.T) {
	prof := profileOf(func(r float64) float64 {
		v := truth(r)
		if r < 0.4 { // a 7% reflection, the amplitude measured on the real set
			v *= 1 + 0.07*math.Cos(r/0.4*math.Pi/2)
		}
		return v
	}, 20)

	v, err := FitRadialVignette(prof, 0.45)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, v.At(0), 0.01, "the centre is reconstructed, not measured")
	assert.InDelta(t, truth(0.2), v.At(0.2), 0.01, "and so is everything under the reflection")

	// Fitting THROUGH the contamination is what this exists to avoid — it drags the model off.
	bad, err := FitRadialVignette(prof, 0)
	require.NoError(t, err)
	assert.Greater(t, math.Abs(bad.At(0.8)-truth(0.8)), math.Abs(v.At(0.8)-truth(0.8)),
		"including the reflection should fit the clean outer field WORSE")
}

func TestRadialVignette_ImageIsNormalisedAndCentred(t *testing.T) {
	v, err := FitRadialVignette(profileOf(truth, 20), 0.45)
	require.NoError(t, err)
	im := v.Image(120, 90, 3)
	require.Equal(t, 3, im.C)

	var sum float64
	for _, p := range im.Pix[0] {
		sum += float64(p)
	}
	assert.InDelta(t, 1.0, sum/float64(len(im.Pix[0])), 1e-4, "a flat is handed over with mean 1")

	centre := im.Pix[0][45*120+60]
	corner := im.Pix[0][0]
	assert.Greater(t, centre, corner, "the centre must be brighter than the corner")
	assert.InDelta(t, truth(1.0), float64(corner)/float64(centre), 0.02, "and by the fitted ratio")

	// Every channel identical: vignetting is geometry, not colour.
	for c := 1; c < 3; c++ {
		assert.Equal(t, im.Pix[0][17], im.Pix[c][17])
	}
}

// The whole reason a radial model is used: it does not know how big the image is, so a flat shot at
// 48 megapixels can calibrate lights at 12 without binning anything.
func TestRadialVignette_IsResolutionIndependent(t *testing.T) {
	v, err := FitRadialVignette(profileOf(truth, 20), 0.45)
	require.NoError(t, err)
	big := v.Image(8064, 6048, 1)
	small := v.Image(4032, 3024, 1)

	// Same normalised position in each must carry the same correction.
	for _, f := range []float64{0.0, 0.25, 0.5} {
		bx, by := int(4032+f*4032), 3024
		sx, sy := int(2016+f*2016), 1512
		assert.InDelta(t, float64(big.Pix[0][by*8064+bx]), float64(small.Pix[0][sy*4032+sx]), 0.01,
			"at %.2f of the half-width", f)
	}
}

func TestFitRadialVignette_Guards(t *testing.T) {
	_, err := FitRadialVignette(&RadialProfile{Level: []float64{1, 1, 1}, MeanR: []float64{0.1, 0.5, 0.9}}, 0.45)
	assert.Error(t, err, "too few bins")

	_, err = FitRadialVignette(profileOf(truth, 20), 0.99)
	assert.Error(t, err, "too little data outside the cut to fit four coefficients")

	// A profile that falls to nothing must not produce a model that divides by zero.
	v, err := FitRadialVignette(profileOf(func(r float64) float64 { return 1 - 0.999*r*r }, 20), 0.45)
	require.NoError(t, err)
	im := v.Image(40, 40, 1)
	for _, p := range im.Pix[0] {
		assert.Greater(t, p, float32(0))
	}
}

func TestRadialProfileOf_MeasuresWhatItShould(t *testing.T) {
	const w, h = 200, 150
	cx, cy := float64(w)/2, float64(h)/2
	maxR := math.Hypot(cx, cy)
	v := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v[y*w+x] = truth(math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) / maxR)
		}
	}
	prof := RadialProfileOf(v, w, h, 20)
	require.Len(t, prof.Level, 20)
	for i, got := range prof.Level {
		// Against the bin's own MEAN radius, not its centre — the outer bins of a rectangle are
		// truncated annuli and their mean radius falls short.
		assert.InDelta(t, truth(prof.MeanR[i]), got, 0.002, "bin %d", i)
	}
	assert.Less(t, prof.MeanR[19], 0.975, "the last bin's mean radius is inside its centre")
	assert.Nil(t, RadialProfileOf(v, w, h, 0))
	assert.Nil(t, RadialProfileOf(v[:10], w, h, 20), "a plane of the wrong length is refused")
}
