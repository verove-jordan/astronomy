package comet

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestTrack_At(t *testing.T) {
	tr, err := NewTrack(Point{0, 0}, 0, Point{100, 50}, 1000)
	require.NoError(t, err)

	assert.Equal(t, Point{0, 0}, tr.At(0))
	assert.Equal(t, Point{100, 50}, tr.At(1000))
	assert.Equal(t, Point{50, 25}, tr.At(500), "linear midpoint")
	assert.Equal(t, Point{20, 10}, tr.At(200))

	dx, dy := tr.Shift(0, Point{50, 25})
	assert.Equal(t, 50.0, dx)
	assert.Equal(t, 25.0, dy)
}

func TestNewTrack_SameTimestampErrors(t *testing.T) {
	_, err := NewTrack(Point{0, 0}, 42, Point{10, 10}, 42)
	require.Error(t, err)
}

func TestFitTrack_RobustToOutliers(t *testing.T) {
	// True linear motion: x = 100 + 0.001·t, y = 50 + 0.0005·t (t in ms).
	var obs []Obs
	for k := 0; k < 30; k++ {
		ts := int64(k * 100_000)
		obs = append(obs, Obs{T: ts, P: Point{X: 100 + 0.001*float64(ts), Y: 50 + 0.0005*float64(ts)}})
	}
	// A few wild bad detections that a 2-point track would be wrecked by.
	obs[5].P = Point{X: 800, Y: 700}
	obs[18].P = Point{X: 0, Y: 0}
	obs[25].P = Point{X: 1200, Y: 50}

	tr, ok := FitTrack(obs)
	require.True(t, ok)
	p := tr.At(1_500_000) // true position there is (1600, 800)
	assert.InDelta(t, 1600.0, p.X, 2.0)
	assert.InDelta(t, 800.0, p.Y, 2.0)
}

func TestFitTrack_TooFew(t *testing.T) {
	_, ok := FitTrack([]Obs{{T: 0, P: Point{1, 1}}})
	assert.False(t, ok)
}

func TestMidTimeAndMiddleFrame(t *testing.T) {
	assert.Equal(t, int64(500), MidTime([]int64{1000, 0, 400}))
	assert.Equal(t, int64(0), MidTime(nil), "empty → 0, no panic")
	// midpoint 500: 520 (dist 20) is closer than 400 (dist 100)
	assert.Equal(t, 3, MiddleFrameIndex([]int64{0, 1000, 400, 520}))
}

func TestTranslate_IntegerShift(t *testing.T) {
	im := fits.NewImage(40, 30, 1)
	im.Pix[0][10*40+10] = 1.0 // a single bright pixel at (10,10)

	out := Translate(im, 5, 3) // content moves right 5, down 3 → bright pixel at (15,13)

	assert.InDelta(t, 1.0, out.Pix[0][13*40+15], 1e-6, "bright pixel shifted to (15,13)")
	assert.InDelta(t, 0.0, out.Pix[0][10*40+10], 1e-6, "original location now empty")
}

func TestTranslate_SubPixelSplitsBilinear(t *testing.T) {
	im := fits.NewImage(40, 30, 1)
	im.Pix[0][10*40+10] = 1.0

	out := Translate(im, 0.5, 0) // half-pixel right → value splits between x=10 and x=11

	assert.InDelta(t, 0.5, out.Pix[0][10*40+10], 1e-6)
	assert.InDelta(t, 0.5, out.Pix[0][10*40+11], 1e-6)
}

// Detect must find the extended coma, not the brighter-per-pixel point stars.
func TestDetect_FindsComaNotStars(t *testing.T) {
	const w, h = 160, 120
	im := fits.NewImage(w, h, 1)
	for i := range im.Pix[0] {
		x, y := i%w, i/w
		im.Pix[0][i] = float32(0.05 + 0.002*float64((x*7+y*13)%5)) // low-level structured background
	}
	// extended coma: a filled disk (radius 8) at (90,60)
	const cx, cy, r = 90, 60, 8
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
				im.Pix[0][y*w+x] += 5.0
			}
		}
	}
	// bright but tiny "stars" elsewhere (higher per-pixel, negligible after blur)
	for _, s := range []struct{ x, y int }{{20, 20}, {140, 30}, {40, 100}} {
		im.Pix[0][s.y*w+s.x] = 10.0
	}

	p, ok := Detect(im, 12, 20)
	require.True(t, ok, "the coma must be detected")
	assert.InDelta(t, float64(cx), p.X, 2.0, "centroid x near the coma")
	assert.InDelta(t, float64(cy), p.Y, 2.0, "centroid y near the coma")
}

// blobImage builds a small frame with a smooth Gaussian blob (the "coma") at (cx,cy).
func blobImage(w, h int, cx, cy, sigma float64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			im.Pix[0][y*w+x] = float32(0.02 + math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)))
		}
	}
	return im
}

func TestAlignToReference_RecoversShift(t *testing.T) {
	const w, h = 200, 160
	ref := blobImage(w, h, 100, 80, 9)
	tests := []struct {
		name                   string
		tx, ty, wantDx, wantDy float64
	}{
		{"integer shift", 104, 75, -4, 5},
		{"sub-pixel shift", 102.5, 81.5, -2.5, -1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := blobImage(w, h, tt.tx, tt.ty, 9) // coma offset from the reference
			dx, dy := AlignToReference(ref, target, Point{100, 80}, 40, 12)
			assert.InDelta(t, tt.wantDx, dx, 0.5, "dx")
			assert.InDelta(t, tt.wantDy, dy, 0.5, "dy")
		})
	}
}

func TestParabola(t *testing.T) {
	tests := []struct {
		name                    string
		cMinus, c0, cPlus, want float64
	}{
		{"symmetric peak → 0", 0.6, 1.0, 0.6, 0},
		{"leans toward +", 0.6, 1.0, 0.8, 0.16667},
		{"leans toward -", 0.8, 1.0, 0.6, -0.16667},
		{"not concave → 0", 1.0, 0.5, 1.0, 0},
		{"flat → 0", 1.0, 1.0, 1.0, 0},
		{"clamped to -0.5", 1.0, 0.9, 0.0, -0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, parabola(tt.cMinus, tt.c0, tt.cPlus), 1e-4)
		})
	}
}

func TestAlignSeeded_AbsoluteSubPixel(t *testing.T) {
	const w, h = 200, 160
	ref := blobImage(w, h, 100, 80, 9)
	// The target's blob is offset; the ABSOLUTE shift that registers target onto ref is (100-tx, 80-ty).
	target := blobImage(w, h, 107.3, 75.6, 9)
	wantDx, wantDy := 100-107.3, 80-75.6 // (-7.3, +4.4)

	t.Run("unseeded recovers the absolute shift to <0.1px", func(t *testing.T) {
		dx, dy := AlignSeeded(ref, target, Point{100, 80}, 40, 10, 0, 0, 0)
		assert.InDelta(t, wantDx, dx, 0.1, "dx")
		assert.InDelta(t, wantDy, dy, 0.1, "dy")
	})
	t.Run("seeded near truth agrees with a tiny search", func(t *testing.T) {
		dx, dy := AlignSeeded(ref, target, Point{100, 80}, 40, 3, 0, -7, 4)
		assert.InDelta(t, wantDx, dx, 0.1, "dx")
		assert.InDelta(t, wantDy, dy, 0.1, "dy")
	})
}

func TestDetect_FlatFieldNoComet(t *testing.T) {
	im := fits.NewImage(64, 64, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = float32(0.1 + 0.001*float64(i%3)) // featureless
	}
	_, ok := Detect(im, 12, 20)
	assert.False(t, ok, "no comet in a flat field")
}
