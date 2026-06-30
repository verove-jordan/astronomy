package pipeline

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fill paints a w×h opaque image and applies per-pixel overrides (index → colour), so a test can
// describe exact clip counts.
func fill(w, h int, base color.RGBA, overrides map[int]color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, base)
		}
	}
	for i, c := range overrides {
		img.Set(i%w, i/w, c)
	}
	return img
}

func TestMetricsFromImage_Clipping(t *testing.T) {
	black := color.RGBA{0, 0, 0, 255}
	white := color.RGBA{255, 255, 255, 255}
	gray := color.RGBA{100, 100, 100, 255}
	over := map[int]color.RGBA{}
	for i := 0; i < 8; i++ { // 8 of 100 pixels crushed to black
		over[i] = black
	}
	for i := 10; i < 14; i++ { // 4 of 100 pixels blown to white
		over[i] = white
	}
	m := metricsFromImage(fill(10, 10, gray, over))

	for c := 0; c < 3; c++ {
		assert.InDelta(t, 0.08, m.BlackClip[c], 1e-9, "channel %d black clip", c)
		assert.InDelta(t, 0.04, m.WhiteClip[c], 1e-9, "channel %d white clip", c)
		assert.InDelta(t, 100.0/255.0, m.Median[c], 1e-9, "channel %d median", c)
	}
}

func TestMetricsFromImage_GreenCast(t *testing.T) {
	// Green dominant: median G well above R/B → positive green cast.
	m := metricsFromImage(fill(8, 8, color.RGBA{40, 90, 40, 255}, nil))
	assert.InDelta(t, (90.0-40.0)/255.0, m.GreenCast, 1e-9)
	assert.Greater(t, m.GreenCast, 0.15)
}

func TestMetricsFromImage_Neutral(t *testing.T) {
	m := metricsFromImage(fill(8, 8, color.RGBA{60, 60, 60, 255}, nil))
	assert.InDelta(t, 0.0, m.GreenCast, 1e-9)
	assert.InDelta(t, 0.0, m.BlackClip[0], 1e-9)
	assert.InDelta(t, 0.0, m.WhiteClip[0], 1e-9)
}

func TestPercentile(t *testing.T) {
	var h [256]uint64
	h[10] = 30
	h[200] = 70
	assert.Equal(t, 10, percentile(h[:], 100, 0.10))  // within the first bucket
	assert.Equal(t, 200, percentile(h[:], 100, 0.50)) // crosses into the second
}
