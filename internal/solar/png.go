package solar

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// png.go writes a finished image out. 16 bits per channel: the finish works in float and a solar
// disc occupies a narrow bright band, so 8-bit quantisation puts visible steps across the surface.

// WritePNG saves a 0..1 image (mono or RGB) as a 16-bit PNG.
func WritePNG(im *fits.Image, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rgba := image.NewRGBA64(image.Rect(0, 0, im.W, im.H))
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			r := chanAt(im, 0, i)
			g, b := r, r
			if im.C >= 3 {
				g, b = chanAt(im, 1, i), chanAt(im, 2, i)
			}
			rgba.SetRGBA64(x, y, color.RGBA64{R: r, G: g, B: b, A: 0xFFFF})
		}
	}
	if err := png.Encode(f, rgba); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return nil
}

// chanAt reads one channel as a 16-bit sample, clamped.
func chanAt(im *fits.Image, c, i int) uint16 {
	if c >= len(im.Pix) {
		return 0
	}
	return uint16(clampF(float64(im.Pix[c][i]), 0, 1)*65535 + 0.5)
}
