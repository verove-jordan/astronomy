package focus

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synthFrame paints Gaussian stars of a known width onto a flat background, so the measurement can
// be checked against the truth rather than against itself.
func synthFrame(w, h int, sigma float64, positions [][2]int, peak float64) []uint16 {
	pix := make([]uint16, w*h)
	for i := range pix {
		pix[i] = 500 // sky + offset
	}
	radius := int(math.Ceil(4 * sigma))
	for _, p := range positions {
		for dy := -radius; dy <= radius; dy++ {
			for dx := -radius; dx <= radius; dx++ {
				x, y := p[0]+dx, p[1]+dy
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				v := peak * math.Exp(-float64(dx*dx+dy*dy)/(2*sigma*sigma))
				sum := float64(pix[y*w+x]) + v
				if sum > 65535 {
					sum = 65535
				}
				pix[y*w+x] = uint16(sum)
			}
		}
	}
	return pix
}

func grid(w, h, step int) [][2]int {
	var out [][2]int
	for y := step; y < h-step; y += step {
		for x := step; x < w-step; x += step {
			out = append(out, [2]int{x, y})
		}
	}
	return out
}

// For a Gaussian, the half-flux diameter is 2·σ·√(2 ln2) ≈ 2.3548σ — the same as its FWHM. That is
// the ground truth the measurement has to reproduce.
func TestMeasure_MatchesTheGaussianTruth(t *testing.T) {
	const w, h = 512, 512
	for _, sigma := range []float64{1.5, 3, 6} {
		pix := synthFrame(w, h, sigma, grid(w, h, 96), 20000)
		res := NewMeter().Measure(pix, w, h, Options{ROIPx: 512, PixelUm: 3.8, FocalMM: 740, ApertureMM: 100})

		require.GreaterOrEqual(t, res.Stars, 3, "sigma %.1f: too few stars measured", sigma)
		assert.True(t, res.Reliable)
		want := 2.3548 * sigma
		assert.InDelta(t, want, res.HFDPx, want*0.2,
			"sigma %.1f: HFD %.2f should be about %.2f", sigma, res.HFDPx, want)
	}
}

func TestMeasure_HFDGrowsWithDefocus(t *testing.T) {
	const w, h = 512, 512
	meter := NewMeter()
	opts := Options{ROIPx: 512, PixelUm: 3.8, FocalMM: 740, ApertureMM: 100}

	sharp := meter.Measure(synthFrame(w, h, 1.5, grid(w, h, 96), 20000), w, h, opts)
	soft := meter.Measure(synthFrame(w, h, 4, grid(w, h, 96), 20000), w, h, opts)
	softest := meter.Measure(synthFrame(w, h, 8, grid(w, h, 96), 20000), w, h, opts)

	assert.Less(t, sharp.HFDPx, soft.HFDPx)
	assert.Less(t, soft.HFDPx, softest.HFDPx)
	assert.Greater(t, sharp.Score, soft.Score, "a sharper frame must score higher")
	assert.Greater(t, soft.Score, softest.Score)
}

// The distance estimate is the number the user actually acts on, so it has to invert the optics
// correctly: blur diameter = defocus ÷ focal ratio.
func TestDefocusUm_InvertsTheBlurCircle(t *testing.T) {
	const pixelUm, ratio = 3.8, 7.4
	// 400 µm out of focus at f/7.4 spreads the light over 400/7.4 = 54 µm ≈ 14.2 px.
	blurPx := 400 / ratio / pixelUm
	hfd := math.Hypot(3, blurPx) // the in-focus HFD adds in quadrature

	got := DefocusUm(hfd, 3, pixelUm, ratio)
	assert.InDelta(t, 400, got, 1, "the focuser distance must invert the blur-circle relation")

	assert.Zero(t, DefocusUm(3, 3, pixelUm, ratio), "at focus, the distance is zero")
	assert.Zero(t, DefocusUm(2, 3, pixelUm, ratio), "sharper than the reference is not a distance")
	assert.Zero(t, DefocusUm(10, 3, 0, ratio), "an unknown pixel size yields no estimate, not a guess")
}

func TestMeasure_TurnsUseTheFocuserCalibration(t *testing.T) {
	const w, h = 512, 512
	pix := synthFrame(w, h, 6, grid(w, h, 96), 20000)
	res := NewMeter().Measure(pix, w, h, Options{
		ROIPx: 512, PixelUm: 3.8, FocalMM: 740, ApertureMM: 100, UmPerTurn: 500,
	})
	require.Positive(t, res.DistanceUm)
	assert.InDelta(t, res.DistanceUm/500, res.Turns, 1e-9)
}

// The direction advice is the honest part: it cannot come from one frame, so it comes from whether
// things are IMPROVING — and from smoothed readings, because seeing alone moves HFD several percent
// frame to frame and a raw comparison would flip its advice while the focuser sits still.
func TestMeter_AdviceFollowsTheTrend(t *testing.T) {
	const w, h = 512, 512
	meter := NewMeter()
	opts := Options{ROIPx: 512, PixelUm: 3.8, FocalMM: 740, ApertureMM: 100, SeeingFloorPx: 2}

	measure := func(sigma float64) Result {
		return meter.Measure(synthFrame(w, h, sigma, grid(w, h, 96), 20000), w, h, opts)
	}

	first := measure(6)
	assert.Equal(t, AdviceFirst, first.Advice, "with no history there is no direction to give")

	// Sitting still: the meter must not invent movement.
	for i := 0; i < 5; i++ {
		measure(6)
	}
	assert.Equal(t, AdviceSteady, measure(6).Advice,
		"an unchanged focuser must read steady, not flap between better and worse")

	// Racking in over several frames.
	var res Result
	for i := 0; i < 4; i++ {
		res = measure(3.5)
	}
	assert.Equal(t, AdviceBetter, res.Advice, "sharper than before: keep turning that way")

	// Racking back out.
	for i := 0; i < 4; i++ {
		res = measure(7)
	}
	assert.Equal(t, AdviceWorse, res.Advice, "softer than before: turn back")

	focused := measure(0.9)
	assert.Equal(t, AdviceAtFocus, focused.Advice)
	assert.Greater(t, focused.Score, 90.0)
}

func TestMeasure_FlagsUnreliableReadings(t *testing.T) {
	const w, h = 512, 512

	blank := make([]uint16, w*h)
	for i := range blank {
		blank[i] = 500
	}
	res := NewMeter().Measure(blank, w, h, Options{ROIPx: 512})
	assert.False(t, res.Reliable, "an empty field is not a focus measurement")
	assert.Equal(t, AdviceUnreliable, res.Advice)
	assert.Zero(t, res.Stars)

	// Saturated stars have clipped flux, which biases HFD low — reporting that as a good focus
	// would send the user the wrong way.
	sat := synthFrame(w, h, 2, grid(w, h, 96), 90000)
	res = NewMeter().Measure(sat, w, h, Options{ROIPx: 512})
	assert.True(t, res.Saturated)
	assert.False(t, res.Reliable)
}

func TestMeasure_ReportsCornerTiltWhenMeasurable(t *testing.T) {
	const w, h = 1024, 1024
	pix := synthFrame(w, h, 2, grid(w, h, 64), 20000)
	res := NewMeter().Measure(pix, w, h, Options{ROIPx: 512, PixelUm: 3.8, FocalMM: 740, ApertureMM: 100})

	require.Len(t, res.TiltCorners, 4)
	// A uniform synthetic field has no tilt: the corners must agree.
	for _, c := range res.TiltCorners {
		assert.InDelta(t, res.TiltCorners[0], c, res.TiltCorners[0]*0.35)
	}
}

func TestMeter_ResetForgetsTheSession(t *testing.T) {
	const w, h = 512, 512
	meter := NewMeter()
	opts := Options{ROIPx: 512, SeeingFloorPx: 2}
	meter.Measure(synthFrame(w, h, 1.2, grid(w, h, 96), 20000), w, h, opts)
	require.Positive(t, meter.best)

	meter.Reset()
	res := meter.Measure(synthFrame(w, h, 5, grid(w, h, 96), 20000), w, h, opts)
	assert.Equal(t, AdviceFirst, res.Advice, "after a filter change the history must not persist")
	assert.InDelta(t, res.HFDPx, meter.best, 1e-9, "the new session's best is this reading, not the old one")
}
