package optics

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// vignetteBase builds a w x h flat of 30000*(1 - k*rn^2), rn = distance-from-center / half-diagonal in
// [0,1]. With the corner-block VignetteDepth metric (blocks sit at rn^2 ~ 0.82), k=0.18 reads ~0.15.
func vignetteBase(w, h int, k float64) []float32 {
	p := make([]float32, w*h)
	cx, cy := float64(w-1)/2, float64(h-1)/2
	half := math.Hypot(cx, cy)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rn := math.Hypot(float64(x)-cx, float64(y)-cy) / half
			p[y*w+x] = float32(30000 * (1 - k*rn*rn))
		}
	}
	return p
}

// addRing multiplies pixels in the annulus [rin,rout] about (cx,cy) by (1-dip) — a dust "donut".
func addRing(p []float32, w, cx, cy int, rin, rout, dip float64) {
	for y := cy - int(rout) - 1; y <= cy+int(rout)+1; y++ {
		for x := cx - int(rout) - 1; x <= cx+int(rout)+1; x++ {
			if x < 0 || y < 0 || x >= w || y*w+x >= len(p) {
				continue
			}
			d := math.Hypot(float64(x-cx), float64(y-cy))
			if d >= rin && d <= rout {
				p[y*w+x] *= float32(1 - dip)
			}
		}
	}
}

// addGauss multiplies in a Gaussian dip (sigma sb, central depth dip) about (cx,cy) — a soft blob.
func addGauss(p []float32, w, cx, cy int, sb, dip float64) {
	rad := int(3 * sb)
	for y := cy - rad; y <= cy+rad; y++ {
		for x := cx - rad; x <= cx+rad; x++ {
			if x < 0 || y < 0 || x >= w || y*w+x >= len(p) {
				continue
			}
			d2 := float64((x-cx)*(x-cx) + (y-cy)*(y-cy))
			p[y*w+x] *= float32(1 - dip*math.Exp(-d2/(2*sb*sb)))
		}
	}
}

// writeFloatMaster writes a 32-bit float mono FITS master from pix and returns its path.
func writeFloatMaster(t *testing.T, dir, name string, w, h int, pix []float32) string {
	t.Helper()
	im := fits.NewImage(w, h, 1)
	copy(im.Pix[0], pix)
	path := filepath.Join(dir, name)
	require.NoError(t, im.WriteFITS(path))
	return path
}

// diskShapeDefect builds a Defect with a binary disk Shape (1 inside radius, 0 outside) filling a
// square bbox of side `side` centered at (cx,cy). AreaPx/Depth are set explicitly. Used by the
// residual/repair tests where an exact, uniform core is needed.
func diskShapeDefect(cx, cy, side, areaPx int, depth float64) Defect {
	half := side / 2
	x0, y0 := cx-half, cy-half
	x1, y1 := x0+side-1, y0+side-1
	r := float64(side) / 2
	shape := make([]float32, side*side)
	for yy := 0; yy < side; yy++ {
		for xx := 0; xx < side; xx++ {
			if math.Hypot(float64(xx)-r+0.5, float64(yy)-r+0.5) <= r-0.5 {
				shape[yy*side+xx] = 1
			}
		}
	}
	return Defect{
		CX: cx, CY: cy, X0: x0, Y0: y0, X1: x1, Y1: y1,
		AreaPx: areaPx, Depth: depth, MeanDepth: depth, Shape: shape,
	}
}

// writeFrame writes a w x h float32 mono light frame and returns its path.
func writeFrame(t *testing.T, dir, name string, w, h int, pix []float32) string {
	t.Helper()
	return writeFloatMaster(t, dir, name, w, h, pix)
}

// imprintDefect multiplies frame pixels inside the defect's core (Shape>0.5) by `mul` — a residual
// left after (imperfect) flat calibration.
func imprintDefect(frame []float32, w, h int, d *Defect, mul float32) {
	sw, sh := shapeDims(d)
	fw, fh := d.X1-d.X0+1, d.Y1-d.Y0+1
	for ry := 0; ry < fh; ry++ {
		fy := d.Y0 + ry
		if fy < 0 || fy >= h {
			continue
		}
		for rx := 0; rx < fw; rx++ {
			fx := d.X0 + rx
			if fx < 0 || fx >= w {
				continue
			}
			if sampleShape(d, sw, sh, rx, ry, fw, fh) > 0.5 {
				frame[fy*w+fx] *= mul
			}
		}
	}
}
