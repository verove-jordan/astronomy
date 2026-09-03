package pipeline

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	scW  = 512
	scH  = 384
	scBg = 0.10
)

// synthStretched builds a stretched-looking RGB image: neutral sky + noise, an R ramp across the
// width (the "banding" residual), a blue disc in the sky (the "blob"), and a bright red square
// object whose chroma must survive the flatten.
func synthStretched(t *testing.T) *fits.Image {
	t.Helper()
	im := fits.NewImage(scW, scH, 3)
	rng := rand.New(rand.NewSource(3))
	for y := 0; y < scH; y++ {
		for x := 0; x < scW; x++ {
			i := y*scW + x
			n := 0.004 * rng.NormFloat64()
			r := scBg + n + 0.02*(float64(x)/scW-0.5) // ±1% of frame: the stretched tint sweep
			g := scBg + n
			b := scBg + n
			if dx, dy := float64(x-140), float64(y-260); dx*dx+dy*dy < 30*30 {
				b += 0.015 // sky-level blue disc
			}
			if x >= 300 && x < 360 && y >= 100 && y < 160 {
				r, g, b = 0.55, 0.35, 0.30 // bright object with real chroma
			}
			im.Pix[0][i], im.Pix[1][i], im.Pix[2][i] = float32(r), float32(g), float32(b)
		}
	}
	return im
}

func skyRG(im *fits.Image, x0, x1 int) float64 {
	var rs, gs []float64
	for y := 180; y < 240; y++ { // sky rows away from the object and the disc
		for x := x0; x < x1; x++ {
			i := y*scW + x
			rs = append(rs, float64(im.Pix[0][i]))
			gs = append(gs, float64(im.Pix[1][i]))
		}
	}
	return median64(rs) / median64(gs)
}

func TestFlattenSkyChroma(t *testing.T) {
	im := synthStretched(t)
	path := filepath.Join(t.TempDir(), "stretch.fits")
	require.NoError(t, im.WriteFITS(path))

	before, err := fits.ReadImage(path)
	require.NoError(t, err)
	note, err := flattenSkyChroma(path, 32)
	require.NoError(t, err)
	assert.Contains(t, note, "sky chroma flattened")

	after, err := fits.ReadImage(path)
	require.NoError(t, err)

	t.Run("horizontal tint removed", func(t *testing.T) {
		rgLeftBefore, rgRightBefore := skyRG(before, 10, 90), skyRG(before, scW-90, scW-10)
		require.Greater(t, rgRightBefore-rgLeftBefore, 0.10) // the seeded sweep is real
		rgLeft, rgRight := skyRG(after, 10, 90), skyRG(after, scW-90, scW-10)
		assert.InDelta(t, 1.0, rgLeft, 0.02)
		assert.InDelta(t, 1.0, rgRight, 0.02)
	})

	t.Run("blue disc neutralized", func(t *testing.T) {
		excess := func(im *fits.Image) float64 {
			var vals []float64
			for y := 245; y < 275; y++ {
				for x := 125; x < 155; x++ {
					i := y*scW + x
					vals = append(vals, float64(im.Pix[2][i])-(float64(im.Pix[0][i])+float64(im.Pix[1][i]))/2)
				}
			}
			return median64(vals)
		}
		require.Greater(t, excess(before), 0.010)
		assert.Less(t, excess(after), excess(before)*0.2)
	})

	t.Run("chromatic blob is neutralized even when it lifts the luminance", func(t *testing.T) {
		// A red-only blob (stray-light halo) raises the mean, but its MIN channel stays at sky —
		// the broadband protection must not shield it. Seeded in its own image to keep the main
		// fixture's expectations unchanged.
		im2 := synthStretched(t)
		for y := 60; y < 140; y++ {
			for x := 420; x < 500; x++ {
				im2.Pix[0][y*scW+x] += 0.08 // R-only: chromatic, not broadband
			}
		}
		p2 := filepath.Join(t.TempDir(), "halo.fits")
		require.NoError(t, im2.WriteFITS(p2))
		_, err := flattenSkyChroma(p2, 32)
		require.NoError(t, err)
		got, err := fits.ReadImage(p2)
		require.NoError(t, err)
		var rg []float64
		for y := 80; y < 120; y++ {
			for x := 440; x < 480; x++ {
				i := y*scW + x
				rg = append(rg, float64(got.Pix[0][i])/float64(got.Pix[1][i]))
			}
		}
		assert.InDelta(t, 1.0, median64(rg), 0.1) // was ~1.8 before the flatten
	})

	t.Run("a bright object is corrected like its surroundings, not exempted", func(t *testing.T) {
		// This subtest used to assert the object came through byte-identical. That contract WAS the
		// coloured-disc bug: exempting an object from a correction every neighbouring pixel receives
		// leaves the sky cast sitting on it as an island, which is what the eye reads as a disc
		// around every star. The properties that actually matter are continuity across the object's
		// edge and the object keeping its OWN colour — both asserted here.
		shift := func(x0, x1 int) float64 {
			var vals []float64
			for y := 110; y < 150; y++ {
				for x := x0; x < x1; x++ {
					i := y*scW + x
					vals = append(vals, float64(before.Pix[0][i]-after.Pix[0][i]))
				}
			}
			return median64(vals)
		}
		onObject, besideIt := shift(310, 350), shift(375, 415)
		assert.InDelta(t, besideIt, onObject, 0.005, "the correction must not step at the object's edge")

		var rg []float64
		for y := 110; y < 150; y++ {
			for x := 310; x < 350; x++ {
				i := y*scW + x
				rg = append(rg, float64(after.Pix[0][i]-after.Pix[1][i]))
			}
		}
		assert.InDelta(t, 0.20, median64(rg), 0.03, "the object is still red, not neutralized")
	})

	t.Run("luminance preserved", func(t *testing.T) {
		worst := 0.0
		for i := range after.Pix[0] {
			mB := (before.Pix[0][i] + before.Pix[1][i] + before.Pix[2][i]) / 3
			mA := (after.Pix[0][i] + after.Pix[1][i] + after.Pix[2][i]) / 3
			worst = math.Max(worst, math.Abs(float64(mA-mB)))
		}
		assert.Less(t, worst, 1e-5)
	})
}

func TestFlattenSkyChroma_NoOps(t *testing.T) {
	t.Run("zero grid", func(t *testing.T) {
		note, err := flattenSkyChroma("/nonexistent.fits", 0)
		require.NoError(t, err)
		assert.Empty(t, note)
	})
	t.Run("mono image", func(t *testing.T) {
		im := fits.NewImage(64, 48, 1)
		path := filepath.Join(t.TempDir(), "mono.fits")
		require.NoError(t, im.WriteFITS(path))
		note, err := flattenSkyChroma(path, 32)
		require.NoError(t, err)
		assert.Empty(t, note)
	})
}
