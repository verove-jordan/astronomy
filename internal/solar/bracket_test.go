package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func member(path string, level float64) Member {
	return Member{FrameProbe: FrameProbe{Path: path, OnDiscMedian: level}, Frames: 1}
}

func TestExposureTiers(t *testing.T) {
	tests := []struct {
		name    string
		members []Member
		want    [][]string // brightest tier first
	}{
		{
			name:    "one exposure is one tier",
			members: []Member{member("a", 0.30), member("b", 0.31), member("c", 0.29)},
			want:    [][]string{{"c", "a", "b"}},
		},
		{
			name:    "metering jitter does not open a tier",
			members: []Member{member("a", 0.30), member("b", 0.34), member("c", 0.41)},
			want:    [][]string{{"a", "b", "c"}},
		},
		{
			name:    "a three-stop bracket splits in two",
			members: []Member{member("bright", 0.32), member("dark", 0.042)},
			want:    [][]string{{"bright"}, {"dark"}},
		},
		{
			name:    "three exposures split in three, brightest first",
			members: []Member{member("mid", 0.08), member("dark", 0.01), member("bright", 0.64)},
			want:    [][]string{{"bright"}, {"mid"}, {"dark"}},
		},
		{
			name:    "a rejected member is not tiered",
			members: []Member{member("keep", 0.30), {FrameProbe: FrameProbe{Path: "drop", OnDiscMedian: 0.04}, Rejected: true}},
			want:    [][]string{{"keep"}},
		},
		{
			name:    "an unmeasurable member joins the reference rather than vanishing",
			members: []Member{member("bright", 0.32), member("dark", 0.04), member("unknown", 0)},
			want:    [][]string{{"bright", "unknown"}, {"dark"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExposureTiers(tt.members, bracketGapStops)

			require.Len(t, got, len(tt.want))
			for i, want := range tt.want {
				paths := make([]string, 0, len(got[i]))
				for _, m := range got[i] {
					paths = append(paths, m.Path)
				}
				assert.ElementsMatch(t, want, paths, "tier %d", i)
			}
		})
	}
}

func TestExposureTiers_Empty(t *testing.T) {
	assert.Nil(t, ExposureTiers(nil, 1))
}

// bracketScene renders a disc plus an off-limb prominence, at a given exposure and with a given
// amount of noise, on the raster a stack would have produced.
func bracketScene(side int, radius, exposure, noise float64, seed int) (*fits.Image, Limb) {
	im := fits.NewImage(side, side, 1)
	c := float64(side-1) / 2
	rnd := seed*1103515245 + 12345
	next := func() float64 {
		rnd = (rnd*1103515245 + 12345) & 0x7fffffff
		return float64(rnd)/float64(0x3fffffff) - 1
	}
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			d := math.Hypot(dx, dy)
			var scene float64
			switch {
			case d <= radius:
				// Disc, limb-darkened, with a filament so there is structure to correlate on.
				scene = 1 - 0.3*(d/radius)*(d/radius)
				if math.Abs(dy-0.3*radius) < 2 && math.Abs(dx) < 0.4*radius {
					scene *= 0.6
				}
			case d < radius*1.08 && dx > 0 && math.Abs(dy) < 0.15*radius:
				scene = 0.02 // a prominence: a couple of percent of the disc
			}
			im.Pix[0][y*side+x] = float32(math.Max(scene*exposure+noise*next(), 0))
		}
	}
	return im, Limb{CX: c, CY: c, R: radius}
}

func TestMergeExposures_SingleTierIsUntouched(t *testing.T) {
	im, l := bracketScene(200, 70, 0.3, 0, 1)

	got, err := MergeExposures([]Exposure{{Master: im, Limb: l, Label: "only"}})

	require.NoError(t, err)
	assert.Same(t, im, got.Master, "one exposure must pass through, not be re-derived")
	assert.Empty(t, got.Notes)
}

func TestMergeExposures_PutsTheTiersOnOneScale(t *testing.T) {
	bright, l := bracketScene(200, 70, 0.32, 0.002, 1)
	dark, dl := bracketScene(200, 70, 0.04, 0.002, 2)

	got, err := MergeExposures([]Exposure{
		{Master: bright, Limb: l, Label: "bright"},
		{Master: dark, Limb: dl, Label: "dark"},
	})

	require.NoError(t, err)
	require.Len(t, got.Tiers, 2)
	// The composite stays in the REFERENCE tier's units, which is what keeps the finish's
	// disc-anchored tone curve and "a prominence is a couple of percent of the disc" working.
	assert.InDelta(t, bracketDiscMedian(bright, l), bracketDiscMedian(got.Master, l), 0.02*bracketDiscMedian(bright, l))
	assert.InDelta(t, 3.0, got.Tiers[1].Stops, 0.5, "the exposure gap is measured, not assumed")
}

func TestMergeExposures_TheDarkerTierDoesNotDragTheProminencesDown(t *testing.T) {
	// The dark tier records the prominence as a handful of quantisation steps: below its noise. A
	// composite that averaged the two equally would halve the prominence and double its noise.
	bright, l := bracketScene(200, 70, 0.32, 0.001, 1)
	dark, dl := bracketScene(200, 70, 0.04, 0.004, 2)

	got, err := MergeExposures([]Exposure{
		{Master: bright, Limb: l, Label: "bright"},
		{Master: dark, Limb: dl, Label: "dark"},
	})

	require.NoError(t, err)
	before := promLevel(bright, l)
	after := promLevel(got.Master, l)
	assert.InDelta(t, before, after, 0.35*before,
		"the prominences must come from the exposure that actually recorded them")
}

func TestMergeExposures_RefusesNothing(t *testing.T) {
	_, err := MergeExposures(nil)
	assert.Error(t, err)

	_, err = MergeExposures([]Exposure{{Master: fits.NewImage(10, 10, 1)}})
	assert.Error(t, err, "a reference without a fitted disc cannot anchor a composite")
}

// bracketDiscMedian is the median level inside half the disc.
func bracketDiscMedian(im *fits.Image, l Limb) float64 {
	var v []float64
	r2 := (0.5 * l.R) * (0.5 * l.R)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			dx, dy := float64(x)-l.CX, float64(y)-l.CY
			if dx*dx+dy*dy <= r2 {
				v = append(v, float64(im.Pix[0][y*im.W+x]))
			}
		}
	}
	return median(v)
}

// promLevel is the median level over the synthetic prominence.
func promLevel(im *fits.Image, l Limb) float64 {
	var v []float64
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			dx, dy := float64(x)-l.CX, float64(y)-l.CY
			d := math.Hypot(dx, dy)
			if d > l.R*1.01 && d < l.R*1.07 && dx > 0 && math.Abs(dy) < 0.1*l.R {
				v = append(v, float64(im.Pix[0][y*im.W+x]))
			}
		}
	}
	return median(v)
}

func TestTonalMap_RecoversAKnownExposureRatio(t *testing.T) {
	const ratio = 8.0
	bright, l := bracketScene(200, 70, 0.32, 0.001, 1)
	dark, _ := bracketScene(200, 70, 0.32/ratio, 0.001, 2)

	m, ok := measureTonalMap(dark.Pix[0], bright.Pix[0], nil, bright.W, bright.H, l)

	require.True(t, ok)
	assert.InDelta(t, ratio, m.gain(), 1.0)
	assert.InDelta(t, math.Log2(ratio), m.stops(), 0.3)
	assert.InDelta(t, 0, m.eval(0), 1e-9, "no light must map to no light")
	assert.Greater(t, m.slope(0.0005), 0.5*ratio,
		"off the disc nothing is fitted, so the mapping keeps the exposure ratio and is weighted "+
			"as the noisier estimate it is")
}

func TestTonalMap_RefusesTooLittleOverlap(t *testing.T) {
	_, ok := measureTonalMap(make([]float32, 16), make([]float32, 16), nil, 4, 4, Limb{CX: 2, CY: 2, R: 2})
	assert.False(t, ok, "a handful of pixels cannot define a tonal mapping")
}
