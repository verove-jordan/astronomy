package comet

import (
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

func TestDetect_FlatFieldNoComet(t *testing.T) {
	im := fits.NewImage(64, 64, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = float32(0.1 + 0.001*float64(i%3)) // featureless
	}
	_, ok := Detect(im, 12, 20)
	assert.False(t, ok, "no comet in a flat field")
}
