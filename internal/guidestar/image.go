package guidestar

import "github.com/verove-jordan/astronomy/internal/fits"

// fullScale is the 16-bit range a camera frame arrives in.
const fullScale = 65535

// ImageFrom wraps a raw camera frame as a normalised image.
//
// The division by full scale is not cosmetic. Star detection judges saturation against an ABSOLUTE
// level — `StarDetectOptions.SatLevel` defaults to 0.9, meaning nine tenths of full scale — so a frame
// handed over in raw 16-bit units reads as saturated everywhere and yields no stars at all. The focus
// meter normalises for the same reason; this keeps the two paths agreeing.
func ImageFrom(pix []uint16, w, h int) (*fits.Image, bool) {
	if w <= 0 || h <= 0 || len(pix) < w*h {
		return nil, false
	}
	im := fits.NewImage(w, h, 1)
	plane := im.Pix[0]
	for i := 0; i < w*h; i++ {
		plane[i] = float32(pix[i]) / fullScale
	}
	return im, true
}
