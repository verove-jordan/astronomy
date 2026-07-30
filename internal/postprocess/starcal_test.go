package postprocess

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// synthField writes a linear RGB FITS with a flat per-channel background plus nStars gaussian stars
// whose per-channel amplitudes follow the given R/G and B/G flux ratios (a "rig colour bias").
func synthField(t *testing.T, nStars int, rg, bg float64) string {
	t.Helper()
	const w, h = 400, 400
	im := fits.NewImage(w, h, 3)
	back := [3]float32{0.050, 0.040, 0.045}
	for c := 0; c < 3; c++ {
		for i := range im.Pix[c] {
			im.Pix[c][i] = back[c]
		}
	}
	amp := [3]float64{0.5 * rg, 0.5, 0.5 * bg} // G amplitude 0.5, others per the ratios
	// Deterministic pseudo-random star grid: cells of 40px, one star per cell until nStars.
	placed := 0
	for cy := 0; cy < h/40 && placed < nStars; cy++ {
		for cx := 0; cx < w/40 && placed < nStars; cx++ {
			px := cx*40 + 12 + (cx*7+cy*13)%16
			py := cy*40 + 12 + (cx*11+cy*5)%16
			for dy := -6; dy <= 6; dy++ {
				for dx := -6; dx <= 6; dx++ {
					x, y := px+dx, py+dy
					if x < 0 || x >= w || y < 0 || y >= h {
						continue
					}
					g := math.Exp(-float64(dx*dx+dy*dy) / (2 * 1.8 * 1.8))
					for c := 0; c < 3; c++ {
						im.Pix[c][y*w+x] += float32(amp[c] * g)
					}
				}
			}
			placed++
		}
	}
	path := filepath.Join(t.TempDir(), "field.fits")
	require.NoError(t, im.WriteFITS(path))
	return path
}

// CONTRACT CHANGE (2026-07-16, task #316): the default anchor moved from a WHITE median star
// (TargetRG/BG = 1.0) to a WARM one (1.10/0.90) — the magnitude-limited field median is a K dwarf,
// and forcing it white over-suppressed R (gains R=0.73/B=0.87 on the Leo Triplet) leaving the
// galaxies/sky green with both downstream green removers gated off. Gains and post-calibration
// ratios now pin the warm anchor.
func TestStarFieldCalibrate_NeutralizesWarmField(t *testing.T) {
	// A red-strong rig: stars measure R/G=1.4, B/G=0.8 → gains pull those toward the warm anchor.
	path := synthField(t, 60, 1.4, 0.8)
	res, err := StarFieldCalibrate(path, StarCalOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied, "expected calibration to apply (found %d stars)", res.Stars)
	assert.GreaterOrEqual(t, res.Stars, starCalMinStars)
	assert.InDelta(t, starCalTargetRG/1.4, res.GainR, 0.08, "R gain should pull the measured ratio to the warm anchor")
	assert.InDelta(t, starCalTargetBG/0.8, res.GainB, 0.08, "B gain should pull the measured ratio to the warm anchor")

	// After calibration the field's own star ratios must read the warm anchor.
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	bg := channelBackgrounds(im)
	stars := detectStars(lumaPlane(im), im.W, im.H)
	rg, bgr, usable := starFluxRatios(im, bg, stars)
	require.GreaterOrEqual(t, usable, starCalMinStars)
	assert.InDelta(t, starCalTargetRG, rg, 0.06, "post-calibration R/G sits at the warm anchor")
	assert.InDelta(t, starCalTargetBG, bgr, 0.06, "post-calibration B/G sits at the warm anchor")

	// An explicit white target still means white — the anchor is only the default.
	path2 := synthField(t, 60, 1.4, 0.8)
	res2, err := StarFieldCalibrate(path2, StarCalOptions{TargetRG: 1, TargetBG: 1})
	require.NoError(t, err)
	require.True(t, res2.Applied)
	assert.InDelta(t, 1/1.4, res2.GainR, 0.08, "explicit white target inverts the ratio exactly")
}

func TestStarFieldCalibrate_TooFewStarsFallsBack(t *testing.T) {
	path := synthField(t, 5, 1.4, 0.8)
	res, err := StarFieldCalibrate(path, StarCalOptions{})
	require.NoError(t, err)
	assert.False(t, res.Applied, "5 stars must not be enough to trust the estimate")

	// The image must be untouched (background unchanged) when not applied.
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	assert.InDelta(t, 0.050, float64(im.Pix[0][0]), 1e-4)
}

func TestStarFieldCalibrate_GainsClamped(t *testing.T) {
	// A 2.5x red excess (still below the saturation filter) needs gain 0.4 — the clamp floor (0.5)
	// must win: a correction that large means detection went wrong, not that the rig is 2.5x red.
	path := synthField(t, 60, 2.5, 1.0)
	res, err := StarFieldCalibrate(path, StarCalOptions{})
	require.NoError(t, err)
	require.True(t, res.Applied)
	assert.Equal(t, starCalGainMin, res.GainR)
}
