package planetary

import (
	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Multi-point ("alignment-point", AP) tuning. After the global lock the residuals are small and local,
// so a modest grid with a small search and a smoothed field corrects atmospheric distortion robustly.
const (
	apGridN     = 8 // apGridN×apGridN grid of alignment points across the frame
	apPatchFrac = 5 // per-AP correlation window half-size, as a percent of the smaller axis
	apMaxShift  = 4 // local residual search around the global seed (px)
	apDiskFrac  = 0.25
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
