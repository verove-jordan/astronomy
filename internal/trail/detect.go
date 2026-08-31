package trail

import (
	"math"
	"sort"
)

// DetectSegments finds up to four straight satellite / aircraft trails in one full-resolution float32
// plane. It max-pools the plane to ≤1024 px, runs a Hough line transform over robustly-bright seed
// pixels, and for each dominant peak confirms an elongated connected component before refining the
// line, width and extent at full resolution. Accepted inliers are erased (set to the background
// median) and the pass repeats. Degenerate input returns nil; it never panics.
func DetectSegments(plane []float32, w, h int, p Params) []Segment {
	if w < 32 || h < 32 || len(plane) != w*h {
		return nil
	}
	grid, gw, gh, f := maxPool(plane, w, h)
	if gw < 16 || gh < 16 {
		return nil
	}
	var out []Segment
	for len(out) < 4 {
		seg, ok := detectOne(grid, gw, gh, plane, w, h, f, p)
		if !ok {
			break
		}
		out = append(out, seg)
	}
	return out
}

// detectOne runs a single detection pass over the (possibly partly-erased) pooled grid, returning the
// refined segment and whether a trail was accepted. On acceptance it erases a generous perpendicular
// band around the streak so the next pass cannot re-detect the same line from surviving edge pixels.
func detectOne(grid []float64, gw, gh int, plane []float32, w, h, f int, p Params) (Segment, bool) {
	med, sigma := robustStats(grid)
	if sigma <= 0 {
		return Segment{}, false
	}
	thr := med + seedK(p)*sigma
	var seeds []int
	for i, v := range grid {
		if v > thr {
			seeds = append(seeds, i)
		}
	}
	if len(seeds) < 16 || float64(len(seeds)) > trailMaxBrite*float64(gw*gh) {
		return Segment{}, false
	}
	cosT, sinT, rhoVal, votes, ok := houghPeak(seeds, gw, gh, p.voteFrac())
	if !ok {
		return Segment{}, false
	}
	inliers, ok := confirmInliers(gw, gh, seeds, cosT, sinT, rhoVal)
	if !ok {
		return Segment{}, false
	}
	seg := refineSegment(plane, w, h, f, gw, inliers, cosT, sinT, rhoVal)
	minDim := gw
	if gh < gw {
		minDim = gh
	}
	seg.Score = float64(votes) / float64(minDim)
	eraseBand(grid, gw, gh, cosT, sinT, rhoVal, inliers, med)
	return seg, true
}

// eraseBand sets every pooled cell within ⊥4 px of the peak line and inside the inlier extent (±4) to
// the background median, wiping the whole streak — core plus edges — before the next pass.
func eraseBand(grid []float64, gw, gh int, cosT, sinT, rhoVal float64, inliers []int, med float64) {
	dirx, diry := -sinT, cosT
	tmin, tmax := math.Inf(1), math.Inf(-1)
	for _, i := range inliers {
		t := float64(i%gw)*dirx + float64(i/gw)*diry
		tmin, tmax = math.Min(tmin, t), math.Max(tmax, t)
	}
	const half = 4.0
	tmin, tmax = tmin-half, tmax+half
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			x, y := float64(gx), float64(gy)
			if math.Abs(x*cosT+y*sinT-rhoVal) > half {
				continue
			}
			if t := x*dirx + y*diry; t >= tmin && t <= tmax {
				grid[gy*gw+gx] = med
			}
		}
	}
}

// seedK is the seed-threshold multiplier: Residual clamps 0.7·K to [1.5,2.5]; Raw is fixed at 5σ.
func seedK(p Params) float64 {
	if p.Mode == Raw {
		if p.RawSeedK > 0 {
			return p.RawSeedK
		}
		return trailBrightK
	}
	k := 0.7 * p.K
	if k < 1.5 {
		k = 1.5
	}
	if k > 2.5 {
		k = 2.5
	}
	return k
}

// maxPool reduces the plane to ≤1024 px on its larger axis via max pooling (preserving thin streaks,
// like fits.ReadDownsampled's Max mode), returning the pooled grid, its dimensions and the factor.
func maxPool(plane []float32, w, h int) (grid []float64, gw, gh, f int) {
	f = 1
	for (w+f-1)/f > 1024 || (h+f-1)/f > 1024 {
		f++
	}
	gw, gh = (w+f-1)/f, (h+f-1)/f
	grid = make([]float64, gw*gh)
	for i := range grid {
		grid[i] = math.Inf(-1)
	}
	for y := 0; y < h; y++ {
		oy, base := y/f, y*w
		for x := 0; x < w; x++ {
			if v := float64(plane[base+x]); v > grid[oy*gw+x/f] {
				grid[oy*gw+x/f] = v
			}
		}
	}
	for i, v := range grid { // guard cells that saw no pixel (shouldn't happen)
		if math.IsInf(v, -1) {
			grid[i] = 0
		}
	}
	return grid, gw, gh, f
}

// robustStats returns the median and the MAD-based sigma (1.4826·MAD) of the grid — the same robust
// spread grade.medianMAD uses.
func robustStats(vals []float64) (median, sigma float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	median = cp[len(cp)/2]
	dev := make([]float64, len(cp))
	for i, v := range cp {
		dev[i] = math.Abs(v - median)
	}
	sort.Float64s(dev)
	return median, 1.4826 * dev[len(dev)/2]
}
