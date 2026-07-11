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

func TestMetricsFromImage_WarmCast(t *testing.T) {
	// Warm sky: red background above green/blue → positive warm cast (measured on the 10th-pct background).
	warm := metricsFromImage(fill(8, 8, color.RGBA{90, 60, 55, 255}, nil))
	assert.InDelta(t, (90.0-(60.0+55.0)/2)/255.0, warm.WarmCast, 1e-9)
	assert.Greater(t, warm.WarmCast, 0.1)
	// Neutral grey → no warm cast.
	assert.InDelta(t, 0.0, metricsFromImage(fill(8, 8, color.RGBA{60, 60, 60, 255}, nil)).WarmCast, 1e-9)
}

func TestMetricsFromImage_Neutral(t *testing.T) {
	m := metricsFromImage(fill(8, 8, color.RGBA{60, 60, 60, 255}, nil))
	assert.InDelta(t, 0.0, m.GreenCast, 1e-9)
	assert.InDelta(t, 0.0, m.BlackClip[0], 1e-9)
	assert.InDelta(t, 0.0, m.WhiteClip[0], 1e-9)
}

func TestMetricsFromImage_SignalCast(t *testing.T) {
	// A bright magenta signal (R,B high, G low) over a dark sky → the 90th-pct lands in the signal →
	// negative SignalCast, even though the sky median is neutral/black.
	black := color.RGBA{0, 0, 0, 255}
	magenta := color.RGBA{200, 80, 200, 255}
	over := map[int]color.RGBA{}
	for i := 0; i < 20; i++ { // 20% bright magenta pixels
		over[i] = magenta
	}
	m := metricsFromImage(fill(10, 10, black, over))
	assert.Less(t, m.SignalCast, -0.2)        // bright signal reads magenta (green deficit)
	assert.InDelta(t, 0.0, m.GreenCast, 1e-9) // sky median stays neutral — the reason we need SignalCast
}

func TestMetricsFromImage_StarSatFrac(t *testing.T) {
	black := color.RGBA{0, 0, 0, 255}
	// Bright, fully-saturated magenta "discs" over a dark sky → most bright cores read as colour discs.
	magenta := color.RGBA{255, 0, 255, 255}
	discs := map[int]color.RGBA{}
	for i := 0; i < 120; i++ {
		discs[i] = magenta
	}
	assert.Greater(t, metricsFromImage(fill(20, 20, black, discs)).StarSatFrac, 0.5,
		"saturated bright cores read as colour discs")

	// Bright WHITE cores (natural, desaturated) → no colour discs.
	white := color.RGBA{255, 255, 255, 255}
	whites := map[int]color.RGBA{}
	for i := 0; i < 120; i++ {
		whites[i] = white
	}
	assert.Less(t, metricsFromImage(fill(20, 20, black, whites)).StarSatFrac, 0.05,
		"white star cores are not colour discs")
}

func TestMetricsFromImage_BgChroma(t *testing.T) {
	// Dark background carpeted with coloured (red/blue) noise → high background chroma mottle.
	red := color.RGBA{40, 0, 0, 255}
	over := map[int]color.RGBA{}
	for i := 0; i < 400; i += 2 { // half dark-red, half the dark-blue base
		over[i] = red
	}
	assert.Greater(t, metricsFromImage(fill(20, 20, color.RGBA{0, 0, 40, 255}, over)).BgChroma, 0.05,
		"coloured noise in the shadows reads as mottle")

	// Neutral dark grey background → no chroma mottle.
	assert.Less(t, metricsFromImage(fill(20, 20, color.RGBA{20, 20, 20, 255}, nil)).BgChroma, 0.01)
}

func TestPercentile(t *testing.T) {
	var h [256]uint64
	h[10] = 30
	h[200] = 70
	assert.Equal(t, 10, percentile(h[:], 100, 0.10))  // within the first bucket
	assert.Equal(t, 200, percentile(h[:], 100, 0.50)) // crosses into the second
}
