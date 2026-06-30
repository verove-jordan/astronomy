package nightscape

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// gradStrength scales how fully removeSkyGradient flattens the sky gradient (0..1). Raised to 0.8 (v5) to
// actually remove the warm horizon light-pollution glow — so the sky background reads homogeneous rather
// than orange-at-the-bottom — kept blotch-free by the now-smooth extrapolated background grid (sparse
// cells are filled from their neighbours, not snapped to a flat level) plus the wide model blur and the
// soft baseline-preserving subtraction.
const gradStrength = 0.8

// removeSkyGradient subtracts the large-scale background gradient (warm horizon glow / light pollution)
// from the sky in place, per channel, WITHOUT the dark foreground biasing the model and WITHOUT eating
// the diffuse Milky Way. The background is sampled as a LOW percentile of the sky pixels (mask>0.5) on a
// coarse grid — the dark sky *between* the stars and band, not the band itself — then 3×3-median-filtered
// to reject any cell dominated by bright structure, bilinearly upscaled, and Gaussian-smoothed into a
// gentle model. Subtracting `strength`·(model − baseline) flattens the spatial colour cast that a single
// global offset cannot, while a per-channel baseline keeps a sky floor. This is the reference recipe's
// remove_gradient made mask-aware. step is the grid cell size in px (≈ width/16; larger = lower-order);
// strength in (0,1] scales how fully the gradient is removed (gentle < 1).
func removeSkyGradient(sky *fits.Image, skyMask []float32, step int, strength float64) {
	w, h := sky.W, sky.H
	if step < 8 || strength <= 0 || len(skyMask) != w*h {
		return
	}
	gw := (w + step - 1) / step
	gh := (h + step - 1) / step
	if gw < 2 || gh < 2 {
		return
	}
	for c := 0; c < sky.C; c++ {
		grid := skyGrid(sky.Pix[c], skyMask, w, h, step, gw, gh) // per-cell low background
		grid = median3(grid, gw, gh)                             // reject bright-structure cells
		model := upscaleBilinear(grid, gw, gh, w, h)
		model = gaussianBlur(model, w, h, float64(step)) // wide blur → a smooth model, no blocky residuals
		baseline := float32(percentile(skyValuesAt(model, skyMask), 5))
		s := float32(strength)
		p := sky.Pix[c]
		for i := range p {
			if v := p[i] - s*(model[i]-baseline); v > 0 {
				p[i] = v
			} else {
				p[i] = 0
			}
		}
	}
}

// skyGrid samples a coarse background grid: each cell's value is a low percentile (P30) of the channel's
// sky pixels (mask>0.5) in that cell — the dark sky level there, ignoring stars and the bright band. A
// cell with too few sky pixels (the sparse-sky tree region near the horizon, where the warm light-
// pollution glow actually lives) is left UNKNOWN and filled by extrapolating the surrounding reliable
// cells (normalized convolution on the coarse grid, gridFillUnknown) — so the gradient is modelled
// continuously down into the low-coverage region and subtracted there too, instead of snapping to a flat
// global level that left the orange bottom under-corrected.
func skyGrid(p, mask []float32, w, h, step, gw, gh int) []float32 {
	const (
		lowPct     = 30.0
		minSamples = 6 // a cell needs at least this many sky pixels to measure its own level
	)
	grid := make([]float32, gw*gh)
	known := make([]float32, gw*gh)
	cell := make([]float32, 0, step*step)
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			cell = cell[:0]
			for y := gy * step; y < (gy+1)*step && y < h; y++ {
				base := y * w
				for x := gx * step; x < (gx+1)*step && x < w; x++ {
					if i := base + x; mask[i] > 0.5 {
						cell = append(cell, p[i])
					}
				}
			}
			if len(cell) >= minSamples {
				grid[gy*gw+gx] = float32(percentile(cell, lowPct))
				known[gy*gw+gx] = 1
			}
		}
	}
	gridFillUnknown(grid, known, gw, gh, float32(percentile(skyValuesAt(p, mask), lowPct)))
	return grid
}

// gridFillUnknown fills the cells where known<0.5 by normalized-convolution extrapolation from the known
// cells — blur the known values and the known mask over the coarse grid, then divide (the same drift-fill
// trick computeCleanSkyStack uses on the full image, here on the gw×gh background grid). A wide sigma
// reaches across a fully-masked horizon strip so the warm gradient continues into it; any cell with no
// blurred support at all falls back to `floor` (the global sky low percentile).
func gridFillUnknown(grid, known []float32, gw, gh int, floor float32) {
	sigma := float64(max(gw, gh)) / 3
	if sigma < 1 {
		sigma = 1
	}
	weighted := make([]float32, gw*gh)
	for i := range grid {
		weighted[i] = grid[i] * known[i] // 0 where unknown (grid is already 0 there)
	}
	num := gaussianBlur(weighted, gw, gh, sigma)
	den := gaussianBlur(known, gw, gh, sigma)
	for i := range grid {
		if known[i] >= 0.5 {
			continue
		}
		if den[i] > 1e-3 {
			grid[i] = num[i] / den[i]
		} else {
			grid[i] = floor
		}
	}
}

// median3 returns a 3×3 median filter of the grid — replaces a cell dominated by bright structure (an
// outlier-high value) with its neighbourhood median, so the band/core doesn't lift the background model.
func median3(grid []float32, gw, gh int) []float32 {
	out := make([]float32, len(grid))
	win := make([]float32, 0, 9)
	for y := 0; y < gh; y++ {
		for x := 0; x < gw; x++ {
			win = win[:0]
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if nx, ny := x+dx, y+dy; nx >= 0 && nx < gw && ny >= 0 && ny < gh {
						win = append(win, grid[ny*gw+nx])
					}
				}
			}
			out[y*gw+x] = medianInPlace(append([]float32(nil), win...))
		}
	}
	return out
}

// upscaleBilinear bilinearly resamples a gw×gh grid (samples at cell centres) to a w×h image.
func upscaleBilinear(grid []float32, gw, gh, w, h int) []float32 {
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)/float64(h)*float64(gh) - 0.5
		y0 := int(math.Floor(fy))
		ty := float32(fy - float64(y0))
		y0c, y1c := clampI(y0, 0, gh-1), clampI(y0+1, 0, gh-1)
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)/float64(w)*float64(gw) - 0.5
			x0 := int(math.Floor(fx))
			tx := float32(fx - float64(x0))
			x0c, x1c := clampI(x0, 0, gw-1), clampI(x0+1, 0, gw-1)
			a, b := grid[y0c*gw+x0c], grid[y0c*gw+x1c]
			cc, d := grid[y1c*gw+x0c], grid[y1c*gw+x1c]
			top := a + tx*(b-a)
			bot := cc + tx*(d-cc)
			out[y*w+x] = top + ty*(bot-top)
		}
	}
	return out
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// skyValuesAt returns the values of v where the sky mask marks sky (>0.5); falls back to all of v.
func skyValuesAt(v, mask []float32) []float32 {
	out := make([]float32, 0, len(v)/2)
	for i, m := range mask {
		if m > 0.5 {
			out = append(out, v[i])
		}
	}
	if len(out) == 0 {
		return v
	}
	return out
}
