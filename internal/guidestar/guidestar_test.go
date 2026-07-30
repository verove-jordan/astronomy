package guidestar

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// starFrame paints a Gaussian star at an exact sub-pixel position on a flat sky. Levels are given in
// ADU and normalised on the way in, so the test data matches the real pipeline's scale.
func starFrame(w, h int, cx, cy, fwhm, peak, sky float64, noise *rand.Rand) *fits.Image {
	im := fits.NewImage(w, h, 1)
	sigma := fwhm / 2.3548
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			v := sky + peak*math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma))
			if noise != nil {
				v += 3 * noise.NormFloat64()
			}
			im.Pix[0][y*im.W+x] = float32(math.Min(v, fullScale) / fullScale)
		}
	}
	return im
}

// The one that matters. A threshold-gated centroid pulls its answer toward whole pixels as edge
// pixels flicker in and out of the sum; that bias is a fraction of a pixel, which on this rig is the
// same size as the entire worm error being measured. Walking a star across a full pixel and demanding
// the measurement follow it linearly is the only way to catch it.
func TestMeasure_IsLinearAcrossAWholePixel(t *testing.T) {
	const trueY = 32.0
	var worst float64
	for step := 0; step <= 20; step++ {
		offset := float64(step) / 20 // 0.00 … 1.00 px
		trueX := 32.0 + offset

		im := starFrame(64, 64, trueX, trueY, 3.2, 4000, 300, nil)
		star, err := Measure(im, 32, 32)
		require.NoError(t, err)

		if d := math.Abs(star.X - trueX); d > worst {
			worst = d
		}
		assert.InDelta(t, trueY, star.Y, 0.05, "the axis that did not move must not appear to")
	}
	assert.Less(t, worst, 0.05,
		"a pixel-locking estimator bends toward whole pixels; this one has to stay straight")
}

func TestMeasure_SurvivesRealisticNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	const trueX, trueY = 40.4, 27.8

	var sumX, sumY float64
	const trials = 30
	for i := 0; i < trials; i++ {
		im := starFrame(80, 64, trueX, trueY, 3.5, 1500, 400, rng)
		star, err := Measure(im, 40, 28)
		require.NoError(t, err)
		sumX += star.X
		sumY += star.Y
	}
	assert.InDelta(t, trueX, sumX/trials, 0.05, "no systematic bias in x")
	assert.InDelta(t, trueY, sumY/trials, 0.05, "no systematic bias in y")
}

// A tracker locked onto a hot pixel reports a mount with no periodic error at all — the most
// convincing possible wrong answer.
func TestPick_RejectsHotPixels(t *testing.T) {
	im := starFrame(96, 96, 60.3, 44.7, 3.2, 2000, 300, nil)
	// A single blazing pixel, brighter than the star, with nothing around it.
	im.Pix[0][30*im.W+30] = 60000

	star, err := Pick(im, Options{})
	require.NoError(t, err)
	assert.InDelta(t, 60.3, star.X, 0.2, "the real star, not the defect")
	assert.InDelta(t, 44.7, star.Y, 0.2)
}

// A clipped star has a flat top, and a flat top sits wherever the plateau happens to be rather than
// where the star is.
func TestPick_RejectsSaturatedStars(t *testing.T) {
	im := starFrame(96, 96, 30.5, 30.5, 3.2, 1200, 300, nil)
	// A much brighter star, clipped flat at full scale.
	sat := starFrame(96, 96, 66.5, 66.5, 4.0, 90000, 0, nil)
	for i, v := range sat.Pix[0] {
		if v > 0 {
			combined := im.Pix[0][i] + v
			if combined > 65535 {
				combined = 65535
			}
			im.Pix[0][i] = combined
		}
	}

	star, err := Pick(im, Options{SaturationFraction: 0.7})
	require.NoError(t, err)
	assert.InDelta(t, 30.5, star.X, 0.3, "the faint unsaturated star is the useful one")
}

// Re-finding must stay local. Silently locking onto a DIFFERENT star injects a step of tens of
// arcseconds mid-run, which then gets fitted as though the worm had done it.
func TestRefind_DoesNotWanderToAnotherStar(t *testing.T) {
	im := starFrame(128, 128, 40, 40, 3.2, 3000, 300, nil)
	other := starFrame(128, 128, 100, 100, 3.2, 9000, 0, nil)
	for i, v := range other.Pix[0] {
		im.Pix[0][i] += v
	}

	star, err := Refind(im, 40.4, 39.6, 0, Options{})
	require.NoError(t, err)
	assert.InDelta(t, 40, star.X, 0.2)
	assert.InDelta(t, 40, star.Y, 0.2)

	// And when the star it was following has gone, it says so rather than grabbing the bright one.
	empty := starFrame(128, 128, 100, 100, 3.2, 9000, 300, nil)
	_, err = Refind(empty, 40, 40, 0, Options{})
	assert.ErrorIs(t, err, ErrNoStar)
}

func TestMeasure_RefusesAStarTooCloseToTheEdge(t *testing.T) {
	im := starFrame(64, 64, 3, 3, 3.2, 4000, 300, nil)
	_, err := Measure(im, 3, 3)
	assert.ErrorIs(t, err, ErrNoStar)
}

func TestPick_ReportsNoStarOnABlankFrame(t *testing.T) {
	im := fits.NewImage(64, 64, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = 300
	}
	_, err := Pick(im, Options{})
	assert.ErrorIs(t, err, ErrNoStar)
}

// Flux is tracked so a star that dews or clouds over can be distrusted before its positions are
// silently folded into a curve.
func TestMeasure_ReportsFluxAndSNR(t *testing.T) {
	bright := starFrame(64, 64, 32, 32, 3.2, 4000, 300, rand.New(rand.NewSource(3)))
	faint := starFrame(64, 64, 32, 32, 3.2, 400, 300, rand.New(rand.NewSource(3)))

	b, err := Measure(bright, 32, 32)
	require.NoError(t, err)
	f, err := Measure(faint, 32, 32)
	require.NoError(t, err)

	assert.Greater(t, b.Flux, f.Flux)
	assert.Greater(t, b.SNR, f.SNR)
	assert.InDelta(t, 3.2, b.HFD, 1.5, "the measured size is in the right ballpark")
}
