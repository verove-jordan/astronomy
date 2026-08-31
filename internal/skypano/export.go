package skypano

// export.go writes a graded canvas out as an image.

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// WritePNG encodes a canvas that Grade has already stretched. keep marks the pixels that carry real
// data — the rest of a mosaic is not black sky, it is nothing, and it is written black so the shape
// of the coverage is honest rather than filled in with whatever the stretch made of zero.
//
// The canvas is display-referred LINEAR light, so this applies the sRGB encoding. maxDim downscales
// by an integer factor when the canvas is larger (0 keeps full size); integer, because a mosaic's
// stars are already near the sampling limit and a fractional resample would soften them.
func WritePNG(im *fits.Image, keep []bool, path string, maxDim int) error {
	if im == nil || im.W <= 0 || im.H <= 0 {
		return fmt.Errorf("skypano: nothing to write to %s", path)
	}
	s := 1
	for maxDim > 0 && (im.W/s > maxDim || im.H/s > maxDim) {
		s++
	}
	w, h := im.W/s, im.H/s
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*s)*im.W + x*s
			if keep != nil && !keep[i] {
				out.Set(x, y, color.RGBA{0, 0, 0, 255})
				continue
			}
			var v [3]uint8
			for k := 0; k < 3; k++ {
				c := im.Pix[k%im.C][i]
				v[k] = uint8(math.Round(255 * math.Min(math.Max(SRGBEncode(float64(c)), 0), 1)))
			}
			out.Set(x, y, color.RGBA{v[0], v[1], v[2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}
