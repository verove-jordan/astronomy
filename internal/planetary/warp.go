package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Catmull-Rom bicubic resampling for the single-pass planetary warp. Bilinear interpolation is a
// triangle-kernel low-pass filter (≈0.81 MTF at half-Nyquist per pass); a Catmull-Rom pass retains
// ≈0.98. Resampling each frame EXACTLY ONCE with this kernel — instead of the old coarse+fine+AP bilinear
// chain (three passes, ≈0.53 retained) — keeps the crater detail the stack used to smear away.

// cubicKernel is the Catmull-Rom (a=-0.5) piecewise-cubic weight for a sample |x| pixels from the target
// position. It is a partition of unity (the four taps sum to 1) and reproduces the exact pixel at x=0.
func cubicKernel(x float64) float64 {
	const a = -0.5
	x = math.Abs(x)
	switch {
	case x <= 1:
		return (a+2)*x*x*x - (a+3)*x*x + 1
	case x < 2:
		return a*x*x*x - 5*a*x*x + 8*a*x - 4*a
	default:
		return 0
	}
}

// cubicWeights returns the four Catmull-Rom taps for a fractional position t in [0,1), for source samples
// at offsets -1,0,+1,+2 from the floored integer position.
func cubicWeights(t float64) [4]float64 {
	return [4]float64{cubicKernel(t + 1), cubicKernel(t), cubicKernel(t - 1), cubicKernel(t - 2)}
}

// sampleCubic samples a plane at fractional (sx,sy) with a separable 4×4 Catmull-Rom kernel. Reads are
// edge-CLAMPED (not zero-filled): zero-fill would darken the bright lunar limb where the 4×4 stencil
// overhangs the frame edge.
func sampleCubic(src []float32, w, h int, sx, sy float64) float32 {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	wx := cubicWeights(sx - float64(x0))
	wy := cubicWeights(sy - float64(y0))
	var sum float64
	for j := 0; j < 4; j++ {
		yy := clampInt(y0-1+j, 0, h-1)
		row := yy * w
		var rowSum float64
		for i := 0; i < 4; i++ {
			xx := clampInt(x0-1+i, 0, w-1)
			rowSum += wx[i] * float64(src[row+xx])
		}
		sum += wy[j] * rowSum
	}
	return float32(sum)
}

// cubicShift returns a copy of im shifted by (dx,dy) with a single Catmull-Rom resample: out(x,y) =
// im(x-dx, y-dy). A positive dx moves content right, a positive dy moves it down. Replaces the bilinear
// comet.Translate on the planetary path (channel-master co-registration).
func cubicShift(im *fits.Image, dx, dy float64) *fits.Image {
	out := fits.NewImage(im.W, im.H, im.C)
	for c := 0; c < im.C; c++ {
		src, dst := im.Pix[c], out.Pix[c]
		for y := 0; y < im.H; y++ {
			row := y * im.W
			for x := 0; x < im.W; x++ {
				dst[row+x] = sampleCubic(src, im.W, im.H, float64(x)-dx, float64(y)-dy)
			}
		}
	}
	return out
}

// warpByGrid resamples im by a per-pixel displacement bilinearly interpolated from the apGridN×apGridN
// (dx,dy) grid: out(x,y) = im(x - dx, y - dy), with a single Catmull-Rom resample of the source pixels.
func warpByGrid(im *fits.Image, dxGrid, dyGrid []float64) *fits.Image {
	out := fits.NewImage(im.W, im.H, im.C)
	for y := 0; y < im.H; y++ {
		gv := (float64(y)+0.5)*apGridN/float64(im.H) - 0.5
		for x := 0; x < im.W; x++ {
			gu := (float64(x)+0.5)*apGridN/float64(im.W) - 0.5
			dx := sampleGrid(dxGrid, gu, gv)
			dy := sampleGrid(dyGrid, gu, gv)
			for c := 0; c < im.C; c++ {
				out.Pix[c][y*im.W+x] = sampleCubic(im.Pix[c], im.W, im.H, float64(x)-dx, float64(y)-dy)
			}
		}
	}
	return out
}

// uniformGrid returns apGridN×apGridN dx/dy grids all equal to (dx,dy) — the global-shift baseline used
// off-disk and when AP alignment is disabled (a pure global cubic translate).
func uniformGrid(dx, dy float64) (dxGrid, dyGrid []float64) {
	n := apGridN * apGridN
	dxGrid = make([]float64, n)
	dyGrid = make([]float64, n)
	for i := 0; i < n; i++ {
		dxGrid[i] = dx
		dyGrid[i] = dy
	}
	return dxGrid, dyGrid
}

// sampleGrid bilinearly samples the apGridN×apGridN grid at fractional index (gu,gv), clamped to range.
func sampleGrid(g []float64, gu, gv float64) float64 {
	gu = clampF(gu, 0, apGridN-1)
	gv = clampF(gv, 0, apGridN-1)
	i0, j0 := int(gu), int(gv)
	i1, j1 := min(i0+1, apGridN-1), min(j0+1, apGridN-1)
	fu, fv := gu-float64(i0), gv-float64(j0)
	v00 := g[j0*apGridN+i0]
	v10 := g[j0*apGridN+i1]
	v01 := g[j1*apGridN+i0]
	v11 := g[j1*apGridN+i1]
	return (v00*(1-fu)+v10*fu)*(1-fv) + (v01*(1-fu)+v11*fu)*fv
}

// clampInt clamps v to [lo,hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampF clamps v to [lo,hi].
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
