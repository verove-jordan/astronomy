package comet

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Translate returns a copy of im shifted by (dx,dy) pixels with sub-pixel bilinear resampling. A positive
// dx moves image content to the right, a positive dy moves it down; samples that fall outside the frame
// read as zero. Used to re-center each star-aligned frame on the comet before stacking.
func Translate(im *fits.Image, dx, dy float64) *fits.Image {
	out := fits.NewImage(im.W, im.H, im.C)
	for c := 0; c < im.C; c++ {
		src, dst := im.Pix[c], out.Pix[c]
		for y := 0; y < im.H; y++ {
			row := y * im.W
			for x := 0; x < im.W; x++ {
				dst[row+x] = bilinear(src, im.W, im.H, float64(x)-dx, float64(y)-dy)
			}
		}
	}
	return out
}

// bilinear samples src at the fractional position (sx,sy); out-of-bounds neighbors contribute zero.
func bilinear(src []float32, w, h int, sx, sy float64) float32 {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	fx := sx - float64(x0)
	fy := sy - float64(y0)
	v00 := at(src, w, h, x0, y0)
	v10 := at(src, w, h, x0+1, y0)
	v01 := at(src, w, h, x0, y0+1)
	v11 := at(src, w, h, x0+1, y0+1)
	top := v00*(1-fx) + v10*fx
	bot := v01*(1-fx) + v11*fx
	return float32(top*(1-fy) + bot*fy)
}

func at(src []float32, w, h, x, y int) float64 {
	if x < 0 || y < 0 || x >= w || y >= h {
		return 0
	}
	return float64(src[y*w+x])
}

// TranslateFile reads a registered FITS frame, shifts it by (dx,dy), and writes the result as 32-bit
// float (the format Siril stacks). Used to build the comet-aligned sequence from the star-aligned frames.
func TranslateFile(inPath, outPath string, dx, dy float64) error {
	im, err := fits.ReadImage(inPath)
	if err != nil {
		return fmt.Errorf("comet translate read %s: %w", inPath, err)
	}
	if err := Translate(im, dx, dy).WriteFITS(outPath); err != nil {
		return fmt.Errorf("comet translate write %s: %w", outPath, err)
	}
	return nil
}
