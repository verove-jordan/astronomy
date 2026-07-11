package planetary

import (
	"sort"

	"math"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Multi-point ("alignment-point", AP) tuning. After the global lock the residuals are small and local,
// so a modest grid with a small search and a smoothed field corrects atmospheric distortion robustly.
const (
	apGridN     = 10 // apGridN×apGridN grid of alignment points across the frame (finer local warp on a full disk)
	apPatchFrac = 5  // per-AP correlation window half-size, as a percent of the smaller axis
	apMaxShift  = 6  // local residual search around the global seed (px) — covers stronger seeing warps
	apDiskFrac  = 0.25
	// apWeightPow shapes the per-AP quality weights: each grid cell is dominated by the frames that
	// were sharpest THERE (AutoStakkert-style multi-point quality), not merely globally sharp.
	apWeightPow = 2.0
	// apWeightMin floors a cell's weight so soft regions still average noise down a little.
	apWeightMin = 0.05
)

// measureAPField overwrites dxGrid/dyGrid at each ON-disk alignment point with that point's ABSOLUTE
// sub-pixel shift onto the reference, from a seeded parabolic ZNCC (seeded at the global drift so the
// ±apMaxShift search covers only the local atmospheric residual). The planes are already blurred, so
// AlignSeeded runs with blur=0. Off-disk points are left at the (gdx,gdy) global baseline the grids
// arrive with — NOT zeroed — so the warp field stays continuous across the dark limb (the whole frame
// drifted by the global shift; zeroing the limb would tear it).
func measureAPField(refBlur, tgtBlur *fits.Image, cx, cy []float64, onDisk []bool, radius int,
	gdx, gdy float64, dxGrid, dyGrid []float64) {
	for k := range cx {
		if !onDisk[k] {
			continue
		}
		dxGrid[k], dyGrid[k] = comet.AlignSeeded(refBlur, tgtBlur,
			comet.Point{X: cx[k], Y: cy[k]}, radius, apMaxShift, 0, gdx, gdy)
	}
}

// apOutlierPx bounds how far one AP's shift may deviate from the median on-disk shift: real seeing
// residuals are small and spatially coherent, so a lone AP several px off is a mislocked correlation
// (flat texture, repeating pattern) that would bend the whole neighbourhood through the smoothing.
const apOutlierPx = 3.0

// rejectAPOutliers resets mislocked on-disk AP shifts back to the global baseline.
func rejectAPOutliers(dxGrid, dyGrid []float64, onDisk []bool, gdx, gdy float64) {
	var xs, ys []float64
	for k, on := range onDisk {
		if on {
			xs = append(xs, dxGrid[k])
			ys = append(ys, dyGrid[k])
		}
	}
	if len(xs) < 3 {
		return
	}
	mx, my := medianOf(xs), medianOf(ys)
	for k, on := range onDisk {
		if !on {
			continue
		}
		if math.Abs(dxGrid[k]-mx) > apOutlierPx || math.Abs(dyGrid[k]-my) > apOutlierPx {
			dxGrid[k], dyGrid[k] = gdx, gdy
		}
	}
}

// medianOf returns the median of v (v is scratch and may be reordered).
func medianOf(v []float64) float64 {
	sort.Float64s(v)
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

// blurPlane returns a 1-channel box-blurred copy of im's first plane, for windowed correlation.
func blurPlane(im *fits.Image, r int) *fits.Image {
	return &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{comet.BoxBlur(im.Pix[0], im.W, im.H, r)}}
}

// apCenters returns the centres of the apGridN×apGridN grid cells (row-major: k = j*apGridN+i).
func apCenters(w, h int) (cx, cy []float64) {
	cx = make([]float64, apGridN*apGridN)
	cy = make([]float64, apGridN*apGridN)
	for j := 0; j < apGridN; j++ {
		for i := 0; i < apGridN; i++ {
			cx[j*apGridN+i] = (float64(i) + 0.5) * float64(w) / apGridN
			cy[j*apGridN+i] = (float64(j) + 0.5) * float64(h) / apGridN
		}
	}
	return cx, cy
}

// apDiskMask marks the alignment points sitting on bright surface (where correlation is meaningful).
// Off-disk points keep the global-shift baseline, so the field smoothly holds across the dark limb.
func apDiskMask(im *fits.Image, cx, cy []float64) []bool {
	bg := lowPercentile(im.Pix[0], 0.2)
	pk := lowPercentile(im.Pix[0], 0.999)
	thr := bg + apDiskFrac*(pk-bg)
	mask := make([]bool, len(cx))
	for k := range cx {
		x, y := int(cx[k]), int(cy[k])
		if x >= 0 && x < im.W && y >= 0 && y < im.H {
			mask[k] = float64(im.Pix[0][y*im.W+x]) > thr
		}
	}
	return mask
}

// smoothGrid replaces each grid value with the mean of its in-bounds 3×3 neighborhood, damping a single
// mis-measured alignment point before the field is interpolated.
func smoothGrid(v []float64) {
	out := make([]float64, len(v))
	for j := 0; j < apGridN; j++ {
		for i := 0; i < apGridN; i++ {
			var sum float64
			var n int
			for dj := -1; dj <= 1; dj++ {
				for di := -1; di <= 1; di++ {
					ii, jj := i+di, j+dj
					if ii < 0 || jj < 0 || ii >= apGridN || jj >= apGridN {
						continue
					}
					sum += v[jj*apGridN+ii]
					n++
				}
			}
			out[j*apGridN+i] = sum / float64(n)
		}
	}
	copy(v, out)
}

// apCellSharpness measures each grid cell's local sharpness (scale-invariant Laplacian variance) on
// an ALIGNED frame — the per-region quality that drives the multi-point stacking weights. Off-disk
// cells return 0 (no meaningful detail to rank).
func apCellSharpness(im *fits.Image, cx, cy []float64, onDisk []bool) []float64 {
	out := make([]float64, len(cx))
	cellW := im.W / apGridN
	cellH := im.H / apGridN
	if cellW < 3 || cellH < 3 {
		return out
	}
	for k := range cx {
		if !onDisk[k] {
			continue
		}
		x0, y0 := int(cx[k])-cellW/2, int(cy[k])-cellH/2
		out[k] = regionLaplacianVariance(im.Pix[0], im.W, im.H, x0, y0, cellW, cellH)
	}
	return out
}

// regionLaplacianVariance is the scale-invariant Laplacian variance over one clamped rectangle of a
// plane: the raw variance normalized by the region's own robust dynamic range squared, so frames of
// different exposure/normalization rank on structure, not brightness.
func regionLaplacianVariance(p []float32, w, h, x0, y0, rw, rh int) float64 {
	x1, y1 := x0+rw, y0+rh
	if x0 < 1 {
		x0 = 1
	}
	if y0 < 1 {
		y0 = 1
	}
	if x1 > w-1 {
		x1 = w - 1
	}
	if y1 > h-1 {
		y1 = h - 1
	}
	if x1-x0 < 3 || y1-y0 < 3 {
		return 0
	}
	var sum, sum2, lo, hi float64
	n := 0
	first := true
	for y := y0; y < y1; y++ {
		row := y * w
		for x := x0; x < x1; x++ {
			c := float64(p[row+x])
			if first {
				lo, hi, first = c, c, false
			} else if c < lo {
				lo = c
			} else if c > hi {
				hi = c
			}
			lap := 4*c - float64(p[row+x-1]) - float64(p[row+x+1]) - float64(p[row-w+x]) - float64(p[row+w+x])
			sum += lap
			sum2 += lap * lap
			n++
		}
	}
	if n == 0 || hi-lo <= 1e-9 {
		return 0
	}
	mean := sum / float64(n)
	v := sum2/float64(n) - mean*mean
	return v / ((hi - lo) * (hi - lo))
}

// apWeightFields normalizes the per-frame per-cell sharpness ACROSS frames (each cell's max over
// all frames = 1) into stacking weight grids: weight = (cellSharp/cellMax)^apWeightPow, floored at
// apWeightMin. Cells with no measurable detail in ANY frame (off-disk) weigh 1 everywhere, so only
// the global frame weight applies there.
func apWeightFields(cellSharp [][]float64) [][]float64 {
	if len(cellSharp) == 0 {
		return nil
	}
	cells := len(cellSharp[0])
	maxPer := make([]float64, cells)
	for _, cs := range cellSharp {
		for k, s := range cs {
			if s > maxPer[k] {
				maxPer[k] = s
			}
		}
	}
	fields := make([][]float64, len(cellSharp))
	for i, cs := range cellSharp {
		f := make([]float64, cells)
		for k, s := range cs {
			if maxPer[k] <= 0 {
				f[k] = 1 // no detail anywhere in this cell → neutral
				continue
			}
			wq := s / maxPer[k]
			if wq < 0 {
				wq = 0
			}
			wv := 1.0
			if wq < 1 {
				wv = pow(wq, apWeightPow)
			}
			if wv < apWeightMin {
				wv = apWeightMin
			}
			f[k] = wv
		}
		fields[i] = f
	}
	return fields
}

// sampleWeightField bilinearly interpolates a frame's apGridN×apGridN weight grid at pixel (x,y).
func sampleWeightField(field []float64, w, h, x, y int) float64 {
	fx := (float64(x)+0.5)/float64(w)*apGridN - 0.5
	fy := (float64(y)+0.5)/float64(h)*apGridN - 0.5
	if fx < 0 {
		fx = 0
	}
	if fy < 0 {
		fy = 0
	}
	maxIdx := float64(apGridN - 1)
	if fx > maxIdx {
		fx = maxIdx
	}
	if fy > maxIdx {
		fy = maxIdx
	}
	i0, j0 := int(fx), int(fy)
	i1, j1 := i0+1, j0+1
	if i1 > apGridN-1 {
		i1 = apGridN - 1
	}
	if j1 > apGridN-1 {
		j1 = apGridN - 1
	}
	tx, ty := fx-float64(i0), fy-float64(j0)
	a := field[j0*apGridN+i0]*(1-tx) + field[j0*apGridN+i1]*tx
	b := field[j1*apGridN+i0]*(1-tx) + field[j1*apGridN+i1]*tx
	return a*(1-ty) + b*ty
}

// pow is a tiny local wrapper (math.Pow) kept here so the hot loops above read clean.
func pow(x, p float64) float64 { return math.Pow(x, p) }
