package optics

import (
	"math"
	"sort"
)

// tileGridN is the side of the square grid of local-mean tiles used for uniformity/vignetting QC.
const tileGridN = 32

// tileGrid computes a tileGridN x tileGridN grid of robust local means. Each tile's pixels are clipped
// to median +/- 3*MAD before averaging so a few hot/cold pixels don't skew the tile. Empty tiles are
// NaN (guarded by the consumers).
func tileGrid(plane []float32, w, h int) []float64 {
	grid := make([]float64, tileGridN*tileGridN)
	for ty := 0; ty < tileGridN; ty++ {
		y0, y1 := ty*h/tileGridN, (ty+1)*h/tileGridN
		for tx := 0; tx < tileGridN; tx++ {
			x0, x1 := tx*w/tileGridN, (tx+1)*w/tileGridN
			grid[ty*tileGridN+tx] = clippedMean(plane, w, x0, y0, x1, y1)
		}
	}
	return grid
}

// clippedMean returns the mean of the [x0,x1)x[y0,y1) region after clipping to median +/- 3*MAD.
func clippedMean(plane []float32, w, x0, y0, x1, y1 int) float64 {
	vals := make([]float64, 0, (x1-x0)*(y1-y0))
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			vals = append(vals, float64(plane[y*w+x]))
		}
	}
	if len(vals) == 0 {
		return math.NaN()
	}
	sort.Float64s(vals)
	med := vals[len(vals)/2]
	devs := make([]float64, len(vals))
	for i, v := range vals {
		devs[i] = math.Abs(v - med)
	}
	sort.Float64s(devs)
	mad := devs[len(devs)/2]
	lo, hi := med-3*mad, med+3*mad
	sum, cnt := 0.0, 0
	for _, v := range vals {
		sum += clampF(v, lo, hi)
		cnt++
	}
	return sum / float64(cnt)
}

// tileExtremes returns the min and max of (tileMean / globalMedian) across the grid. NaN tiles and a
// non-positive median are skipped; when nothing is usable it returns (1,1).
func tileExtremes(grid []float64, globalMed float64) (min, max float64) {
	if globalMed <= 0 {
		return 1, 1
	}
	min, max = math.Inf(1), math.Inf(-1)
	for _, m := range grid {
		if math.IsNaN(m) {
			continue
		}
		r := m / globalMed
		if r < min {
			min = r
		}
		if r > max {
			max = r
		}
	}
	if math.IsInf(min, 1) {
		return 1, 1
	}
	return min, max
}

// vignetteDepth = 1 - (mean of the four 3x3 corner tile-blocks) / (mean of the central 4x4 tile-block).
// It measures how far the field edges fall below the center on the tile grid.
func vignetteDepth(grid []float64) float64 {
	const g = tileGridN
	corners := (blockMean(grid, 0, 0, 3, 3) +
		blockMean(grid, g-3, 0, 3, 3) +
		blockMean(grid, 0, g-3, 3, 3) +
		blockMean(grid, g-3, g-3, 3, 3)) / 4
	center := blockMean(grid, g/2-2, g/2-2, 4, 4)
	if center <= 0 || math.IsNaN(center) || math.IsNaN(corners) {
		return 0
	}
	return 1 - corners/center
}

// blockMean averages a bw x bh block of the tile grid starting at (tx,ty), skipping NaN tiles.
func blockMean(grid []float64, tx, ty, bw, bh int) float64 {
	sum, cnt := 0.0, 0
	for y := ty; y < ty+bh; y++ {
		for x := tx; x < tx+bw; x++ {
			v := grid[y*tileGridN+x]
			if math.IsNaN(v) {
				continue
			}
			sum += v
			cnt++
		}
	}
	if cnt == 0 {
		return math.NaN()
	}
	return sum / float64(cnt)
}

// deadFraction is the fraction of plane pixels below half the global median — dead/occluded pixels.
func deadFraction(plane []float32, globalMed float64) float64 {
	if len(plane) == 0 {
		return 0
	}
	thr := 0.5 * globalMed
	dead := 0
	for _, v := range plane {
		if float64(v) < thr {
			dead++
		}
	}
	return float64(dead) / float64(len(plane))
}
