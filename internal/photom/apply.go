package photom

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// applyTransform rewrites every path in place as p = Scale*p + Offset via OverwriteData. It honours
// context cancellation between frames and returns the first read/write error so the caller can abort
// the group. OverwriteData validates the on-disk header before touching bytes, so a non-float32 (e.g.
// 16-bit) frame is rejected without modification.
func applyTransform(ctx context.Context, paths []string, t Transform) error {
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		im, err := fits.ReadImage(p)
		if err != nil {
			return err
		}
		applyToImage(im, t)
		if err := im.OverwriteData(p); err != nil {
			return err
		}
	}
	return nil
}

// applyToImage maps every pixel of every channel in place as p = Scale*p + Offset.
func applyToImage(im *fits.Image, t Transform) {
	for c := range im.Pix {
		plane := im.Pix[c]
		for i := range plane {
			plane[i] = float32(t.Scale*float64(plane[i]) + t.Offset)
		}
	}
}
