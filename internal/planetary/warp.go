package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Catmull-Rom bicubic resampling for the single-pass planetary warp. Resampling each frame EXACTLY
// ONCE with this kernel — instead of the old coarse+fine+AP bilinear chain (three passes, ≈0.53 MTF
// retained) — keeps the crater detail the stack used to smear away. The kernel itself lives in
// internal/imgops (shared with the mosaic panel reprojector); these wrappers keep the planetary
// call sites and pinning tests on the package-local names.

func cubicKernel(x float64) float64 { return imgops.CubicKernel(x) }

func cubicWeights(t float64) [4]float64 { return imgops.CubicWeights(t) }

func sampleCubic(src []float32, w, h int, sx, sy float64) float32 {
	return imgops.SampleCubic(src, w, h, sx, sy)
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

// gridSize recovers the side length of a square AP grid from its flat slice (grids are always n×n).
func gridSize(g []float64) int {
	n := int(math.Round(math.Sqrt(float64(len(g)))))
	if n < 1 {
		return 1
	}
	return n
}

// warpByGrid resamples im by a per-pixel displacement bilinearly interpolated from the n×n (dx,dy)
// grid: out(x,y) = im(x - dx, y - dy), with a single Catmull-Rom resample of the source pixels.
// It is the native-raster case of warpByGridScaled (drizzle.go), bit-identical at scale 1.
func warpByGrid(im *fits.Image, dxGrid, dyGrid []float64) *fits.Image {
	return warpByGridScaled(im, dxGrid, dyGrid, 1)
}

// uniformGrid returns n×n dx/dy grids all equal to (dx,dy) — the global-shift baseline used
// off-disk and when AP alignment is disabled (a pure global cubic translate).
func uniformGrid(dx, dy float64, n int) (dxGrid, dyGrid []float64) {
	cells := n * n
	dxGrid = make([]float64, cells)
	dyGrid = make([]float64, cells)
	for i := 0; i < cells; i++ {
		dxGrid[i] = dx
		dyGrid[i] = dy
	}
	return dxGrid, dyGrid
}

// sampleGrid bilinearly samples an n×n grid at fractional index (gu,gv), clamped to range.
func sampleGrid(g []float64, gu, gv float64, n int) float64 {
	gu = clampF(gu, 0, float64(n-1))
	gv = clampF(gv, 0, float64(n-1))
	i0, j0 := int(gu), int(gv)
	i1, j1 := min(i0+1, n-1), min(j0+1, n-1)
	fu, fv := gu-float64(i0), gv-float64(j0)
	v00 := g[j0*n+i0]
	v10 := g[j0*n+i1]
	v01 := g[j1*n+i0]
	v11 := g[j1*n+i1]
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
