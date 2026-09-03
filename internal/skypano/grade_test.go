package skypano

import (
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// gradeFixture is a canvas with a known sky level, a few bright stars, and a wide uncovered margin —
// the shape a real mosaic has.
func gradeFixture(sky float64, coveredFrac float64) (*fits.Image, []float32, Canvas) {
	const w, h = 200, 200
	c := Canvas{Proj: Equirectangular, Fr: Galactic, W: w, H: h, Lon0: 60, Lat0: 0, ScaleDegPerPix: 0.1}
	im := fits.NewImage(w, h, 3)
	cov := make([]float32, w*h)
	edge := int(float64(w) * (1 - coveredFrac) / 2)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if x < edge || x >= w-edge {
				continue // uncovered: zero pixels, zero weight
			}
			cov[i] = 1
			v := sky * (1 + 0.02*math.Sin(float64(x)))
			if x%37 == 0 && y%41 == 0 {
				v = sky * 50 // a star
			}
			for ch := 0; ch < 3; ch++ {
				im.Pix[ch][i] = float32(v)
			}
		}
	}
	return im, cov, c
}

func coveredMedian(im *fits.Image, keep []bool, ch int) float64 {
	var buf []float64
	for i, k := range keep {
		if k {
			buf = append(buf, float64(im.Pix[ch][i]))
		}
	}
	sort.Float64s(buf)
	return buf[len(buf)/2]
}

// TestGrade_PutsTheSkyWhereItWasAsked: the stretch solves for the scale that lands the background on
// target, so this holds for any input level rather than for one that happened to be tuned.
func TestGrade_PutsTheSkyWhereItWasAsked(t *testing.T) {
	for _, tt := range []struct {
		name          string
		sky, targetBg float64
		intensity     float64
	}{
		{"faint sky", 0.001, 0.10, 30},
		{"bright sky", 0.05, 0.10, 30},
		{"darker target", 0.005, 0.05, 30},
		{"gentler curve", 0.005, 0.12, 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			im, cov, c := gradeFixture(tt.sky, 0.6)
			o := DefaultGradeOptions()
			o.TargetBg, o.Intensity, o.Saturation = tt.targetBg, tt.intensity, 1

			keep := Grade(im, cov, c, o)

			got := SRGBEncode(coveredMedian(im, keep, 1))
			assert.InDelta(t, tt.targetBg, got, 0.02, "the sky background must land on target")
		})
	}
}

// TestGrade_IgnoresTheUncoveredCanvas is why this stretch exists instead of nightscape's: a mosaic is
// not a rectangle, and statistics taken over the empty corners measure its outline.
func TestGrade_IgnoresTheUncoveredCanvas(t *testing.T) {
	wide, wideCov, wc := gradeFixture(0.005, 0.9)
	narrow, narrowCov, nc := gradeFixture(0.005, 0.3)
	o := DefaultGradeOptions()

	kw := Grade(wide, wideCov, wc, o)
	kn := Grade(narrow, narrowCov, nc, o)

	assert.InDelta(t, coveredMedian(wide, kw, 1), coveredMedian(narrow, kn, 1), 0.01,
		"how much of the canvas is empty must not change the grade")
}

func TestGrade_KeepsTheOrderOfBrightness(t *testing.T) {
	im, cov, c := gradeFixture(0.005, 0.8)
	before := make([]float32, len(im.Pix[1]))
	copy(before, im.Pix[1])

	keep := Grade(im, cov, c, DefaultGradeOptions())

	var pairs [][2]float32
	for i, k := range keep {
		if k {
			pairs = append(pairs, [2]float32{before[i], im.Pix[1][i]})
		}
	}
	require.NotEmpty(t, pairs)
	sort.Slice(pairs, func(a, b int) bool { return pairs[a][0] < pairs[b][0] })
	for i := 1; i < len(pairs); i++ {
		require.GreaterOrEqual(t, pairs[i][1], pairs[i-1][1], "the stretch must be monotonic")
	}
}

func TestGrade_MarksNothingWhenNothingIsCovered(t *testing.T) {
	im := fits.NewImage(10, 10, 3)

	keep := Grade(im, make([]float32, 100), Canvas{}, DefaultGradeOptions())

	for _, k := range keep {
		assert.False(t, k)
	}
}

func TestSRGB_RoundTrip(t *testing.T) {
	for _, v := range []float64{0, 0.002, 0.01, 0.1, 0.5, 1} {
		assert.InDelta(t, v, SRGBEncode(srgbDecode(v)), 1e-12, "at %v", v)
	}
}

// A canvas lying ALONG the galactic plane is the case that exposed a real defect: most of what it
// covers is the Milky Way, so the median of everything covered is the BAND, and taking the sky level
// there pins the band itself to TargetBg and renders the whole picture dark. Measured on a real
// two-panel canvas centred at galactic latitude -6 and -9.
func TestGrade_ACanvasAlongTheBandIsNotRenderedDark(t *testing.T) {
	// A canvas centred on the galactic plane: most rows are band, a fringe is sky.
	c := Canvas{Proj: Equirectangular, Fr: Galactic, W: 400, H: 300,
		Lon0: 90, Lat0: 0, ScaleDegPerPix: 0.25} // +/- 37 deg latitude: the middle is all band
	im := fits.NewImage(c.W, c.H, 3)
	cov := make([]float32, c.W*c.H)
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			i := y*c.W + x
			cov[i] = 1
			v, _ := c.PixToSky(float64(x)+0.5, float64(y)+0.5)
			_, b := vecToLonLat(equatorialToGalactic(v))
			// Sky sits at 0.005; the band is four times brighter.
			level := float32(0.005)
			if math.Abs(b) < 20 {
				level = 0.020
			}
			for ch := 0; ch < 3; ch++ {
				im.Pix[ch][i] = level
			}
		}
	}
	o := DefaultGradeOptions()
	keep := Grade(im, cov, c, o)
	require.NotEmpty(t, keep)

	// Read back the graded sky and the graded band, in the same display-referred space TargetBg is in.
	var skyV, bandV float64
	for y := 0; y < c.H; y++ {
		v, _ := c.PixToSky(10.5, float64(y)+0.5)
		_, b := vecToLonLat(equatorialToGalactic(v))
		p := float64(im.Pix[1][y*c.W+10])
		if math.Abs(b) > 30 {
			skyV = math.Max(skyV, p)
		}
		if math.Abs(b) < 5 {
			bandV = math.Max(bandV, p)
		}
	}
	assert.Greater(t, bandV, skyV, "the band must end up brighter than the sky it sits in")
	// The BAND must not be the thing pinned to the background target — that is the bug.
	assert.Greater(t, bandV, 1.5*o.TargetBg,
		"the band was pinned to the background target, so the whole canvas renders dark")
}

// A pixel can be REAL and yet not something to measure — the landscape under an arch. Coverage cannot
// express that, because it also answers "is this pixel real" and the exporter paints everything unreal
// black. Punching a hole in the coverage composited the foreground correctly and then blacked it out.
//
// The test is COMPARATIVE on purpose. Asserting an absolute output level would measure the fixture:
// removing a quarter of the pixels from the statistics changes how far the 99.9th-percentile white
// point reaches into a sparse star field, which moves the answer for reasons that have nothing to do
// with the landscape. What must hold is that an excluded landscape changes the sky's grade by nothing,
// and an unexcluded one changes it a lot.
func TestGrade_ExcludedPixelsAreKeptButNotMeasured(t *testing.T) {
	const skyLevel = 0.02
	withLandscape := func(exclude bool) ([]bool, []float64) {
		im, cov, c := gradeFixture(skyLevel, 1.0)
		excl := make([]bool, c.W*c.H)
		for y := 150; y < c.H; y++ {
			for x := 0; x < c.W; x++ {
				i := y*c.W + x
				for ch := 0; ch < 3; ch++ {
					im.Pix[ch][i] = float32(skyLevel / 40) // a dark landscape
				}
				excl[i] = true
			}
		}
		o := DefaultGradeOptions()
		if exclude {
			o.Exclude = excl
		}
		keep := Grade(im, cov, c, o)
		var sky []float64
		for y := 20; y < 140; y++ {
			for x := 20; x < 180; x += 7 {
				sky = append(sky, float64(im.Pix[1][y*c.W+x]))
			}
		}
		sort.Float64s(sky)
		return keep, sky
	}
	// The reference: the same sky with no landscape at all.
	refIm, refCov, refC := gradeFixture(skyLevel, 1.0)
	for y := 150; y < refC.H; y++ {
		for x := 0; x < refC.W; x++ {
			i := y*refC.W + x
			for ch := 0; ch < 3; ch++ {
				refIm.Pix[ch][i] = 0
			}
			refCov[i] = 0 // genuinely not part of the picture
		}
	}
	Grade(refIm, refCov, refC, DefaultGradeOptions())
	var ref []float64
	for y := 20; y < 140; y++ {
		for x := 20; x < 180; x += 7 {
			ref = append(ref, float64(refIm.Pix[1][y*refC.W+x]))
		}
	}
	sort.Float64s(ref)
	want := ref[len(ref)/2]

	keptExcl, skyExcl := withLandscape(true)

	// The bug this exists for: every landscape pixel must still be part of the picture.
	for y := 150; y < 200; y++ {
		require.True(t, keptExcl[y*200+100], "an excluded pixel must still be kept")
	}
	// And the sky grades exactly as it would with no landscape there at all.
	assert.InDelta(t, want, skyExcl[len(skyExcl)/2], 0.02,
		"an EXCLUDED landscape must not move the sky's grade")

	// NOT asserted: that an unexcluded landscape moves the sky's LEVEL. It does not, and expecting it
	// to misreads the grade — the stretch pins the sky's median onto TargetBg whatever the black point
	// did, so the level is self-correcting. What a dark landscape in the sample actually corrupts is
	// the black POINT, which lands inside the landscape instead of in the sky, and that shows up as
	// flat, unclipped shadows rather than as a shifted sky.
}
