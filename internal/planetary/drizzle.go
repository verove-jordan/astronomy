package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Drizzle-like super-resolution: hundreds of short frames land on the output grid at
// sub-pixel offsets (natural seeing/tracking dither), so accumulating them onto a FINER grid
// genuinely adds resolution instead of averaging it away. Every aligned frame is resampled
// exactly once — the same single Catmull-Rom pass as the native path, just onto a scaled
// grid: displacement fields stay in NATIVE pixel units and are measured at native
// resolution; only the output raster is scaled. The stack accumulators size themselves from
// the aligned frames, so the whole downstream chain (per-AP weights, sigma-clip, masters,
// finish, earthshine) runs at the scaled size unchanged.

// drizzleScales are the supported output grids; SnapDrizzle snaps any requested value to the
// nearest one (≤0 — an unset legacy Options — means native).
var drizzleScales = [...]float64{1, 1.5, 2}

// SnapDrizzle normalizes a drizzle_scale knob value: ≤0 → 1 (native), anything else snaps to
// the nearest supported scale. Exported for the pipeline knob clamp.
func SnapDrizzle(v float64) float64 {
	if v <= 0 {
		return 1
	}
	best, bestDist := drizzleScales[0], math.Abs(v-drizzleScales[0])
	for _, s := range drizzleScales[1:] {
		if d := math.Abs(v - s); d < bestDist {
			best, bestDist = s, d
		}
	}
	return best
}

// scaledDim is a raster dimension under a drizzle scale.
func scaledDim(n int, scale float64) int {
	return int(math.Round(float64(n) * scale))
}

// scaleCoords maps native pixel coordinates onto the scaled raster (the AP-cell centres used
// to score SCALED aligned frames). Scale 1 returns the input slice untouched.
func scaleCoords(v []float64, scale float64) []float64 {
	if scale == 1 {
		return v
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = (x+0.5)*scale - 0.5
	}
	return out
}

// resamplePlaneTo resizes an image to exact target dimensions with one Catmull-Rom pass —
// used for the double-stack measurement reference (the scaled pass-1 master brought back to
// the frames' native raster) and the acceptance gate's native-side comparison.
func resamplePlaneTo(im *fits.Image, ow, oh int) *fits.Image {
	if ow == im.W && oh == im.H {
		return im
	}
	out := fits.NewImage(ow, oh, im.C)
	sx := float64(im.W) / float64(ow)
	sy := float64(im.H) / float64(oh)
	for c := 0; c < im.C; c++ {
		for y := 0; y < oh; y++ {
			v := (float64(y)+0.5)*sy - 0.5
			for x := 0; x < ow; x++ {
				u := (float64(x)+0.5)*sx - 0.5
				out.Pix[c][y*ow+x] = sampleCubic(im.Pix[c], im.W, im.H, u, v)
			}
		}
	}
	return out
}

// resamplePlane resizes an image by a scale factor (see resamplePlaneTo).
func resamplePlane(im *fits.Image, scale float64) *fits.Image {
	return resamplePlaneTo(im, scaledDim(im.W, scale), scaledDim(im.H, scale))
}

// warpByGridScaled resamples im by the displacement field onto a `scale`× output raster:
// out(x,y) = im(u − dx(u,v), v − dy(u,v)) with (u,v) the output pixel's NATIVE coordinates.
// The field and its grid stay in native units — exactly warpByGrid's semantics at scale 1
// (bit-identical there), with the single Catmull-Rom resample landing on the finer grid.
func warpByGridScaled(im *fits.Image, dxGrid, dyGrid []float64, scale float64) *fits.Image {
	n := gridSize(dxGrid)
	ow, oh := scaledDim(im.W, scale), scaledDim(im.H, scale)
	out := fits.NewImage(ow, oh, im.C)
	for y := 0; y < oh; y++ {
		v := (float64(y)+0.5)/scale - 0.5
		gv := (v+0.5)*float64(n)/float64(im.H) - 0.5
		for x := 0; x < ow; x++ {
			u := (float64(x)+0.5)/scale - 0.5
			gu := (u+0.5)*float64(n)/float64(im.W) - 0.5
			dx := sampleGrid(dxGrid, gu, gv, n)
			dy := sampleGrid(dyGrid, gu, gv, n)
			for c := 0; c < im.C; c++ {
				out.Pix[c][y*ow+x] = sampleCubic(im.Pix[c], im.W, im.H, u-dx, v-dy)
			}
		}
	}
	return out
}
