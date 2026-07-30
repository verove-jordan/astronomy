package mosaic

// Panel → canvas reprojection.
//
// ROW-ORDER CONVENTION (verified against internal/fits and internal/annotate): fits.Image stores
// rows in FILE order (Pix[c][y*W+x], row 0 = the file's first row), and the FITS WCS standard
// addresses pixels in that same storage order — the axis-2 pixel coordinate IS the array row index
// (+1 for the 1-based FITS origin). WCS.SkyToPix/PixToSky already speak 0-based axis coordinates,
// so the y they exchange indexes Pix rows directly: NO flip is applied anywhere in this package,
// on either the panel or the canvas side. ROWORDER is a display hint (Siril writes BOTTOM-UP, this
// repo's WriteFITS stamps TOP-DOWN) and does not move the WCS. As long as every panel was solved
// by the same Siril against the same storage convention, identity keeps the panels mutually
// consistent — and the canvas header the pipeline emits (WriteFITSWith + canvas WCS.Cards)
// describes exactly the array AssembleChannel wrote. internal/annotate's chooseFlip stays the
// empirical guard for absolute orientation downstream.

import (
	"context"
	"math"

	"golang.org/x/sync/errgroup"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// accumulatePanel adds one panel into the canvas sum/weight accumulators: for each canvas pixel in
// the panel's projected bbox, inverse-map canvas→sky→panel, Catmull-Rom-sample the panel and
// bilinear-sample its weight, apply the panel's photometric correction (v·gain+offset), and
// accumulate sum += w·v, wsum += w. Row-parallel via errgroup bounded by workers; each band owns
// disjoint canvas rows, so the accumulators need no locking.
func accumulatePanel(ctx context.Context, p PanelImage, wm *WeightMap, gain, offset float64, canvas CanvasSpec, sum, wsum []float32, workers int) error {
	x0, y0, x1, y1, ok := panelCanvasBBox(p, canvas)
	if !ok {
		return nil
	}
	if workers <= 0 {
		workers = 1
	}
	g, gctx := errgroup.WithContext(ctx)
	band := (y1 - y0 + workers - 1) / workers
	for b := y0; b < y1; b += band {
		b0, b1 := b, min(b+band, y1)
		g.Go(func() error {
			return accumulateRows(gctx, p, wm, gain, offset, canvas, sum, wsum, x0, x1, b0, b1)
		})
	}
	return g.Wait()
}

// accumulateRows processes the canvas rows [y0,y1) of one band. Samples landing outside the
// panel's pixel grid or on zero blend weight are skipped, as are non-finite resampled values.
func accumulateRows(ctx context.Context, p PanelImage, wm *WeightMap, gain, offset float64, canvas CanvasSpec, sum, wsum []float32, x0, x1, y0, y1 int) error {
	pw, ph := p.Image.W, p.Image.H
	plane := p.Image.Pix[0]
	for y := y0; y < y1; y++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := y * canvas.W
		for x := x0; x < x1; x++ {
			ra, dec := canvas.WCS.PixToSky(float64(x), float64(y))
			sx, sy, ok := p.WCS.SkyToPix(ra, dec)
			if !ok || sx < 0 || sy < 0 || sx > float64(pw-1) || sy > float64(ph-1) {
				continue
			}
			wv := bilinearPlane(wm.Pix, wm.W, wm.H, sx, sy)
			if wv <= 0 {
				continue
			}
			v := float64(imgops.SampleCubic(plane, pw, ph, sx, sy))
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			sum[row+x] += wv * float32(v*gain+offset)
			wsum[row+x] += wv
		}
	}
	return nil
}

// bilinearPlane samples a plane at fractional (x,y) with bilinear interpolation, edge-clamped —
// the smooth, overshoot-free sampler for weight maps and photometric measurements (Catmull-Rom
// would ring at the star cores the photometric fit must survive).
func bilinearPlane(pix []float32, w, h int, x, y float64) float32 {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := float32(x - float64(x0))
	fy := float32(y - float64(y0))
	xa, xb := clampInt(x0, 0, w-1), clampInt(x0+1, 0, w-1)
	ya, yb := clampInt(y0, 0, h-1), clampInt(y0+1, 0, h-1)
	top := pix[ya*w+xa]*(1-fx) + pix[ya*w+xb]*fx
	bot := pix[yb*w+xa]*(1-fx) + pix[yb*w+xb]*fx
	return top*(1-fy) + bot*fy
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
