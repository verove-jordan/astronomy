package noise

import (
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	tileSize        = 64     // side of a measurement tile, in pixels
	measureScales   = 4      // starlet scales used by Measure
	snrSubsampleMax = 200000 // cap on samples used for plane percentiles (speed)
)

// Report is the noise measurement of an image plane (plane 0): a global robust sigma plus a
// per-tile sigma grid for the heatmap and coarse background / SNR context.
type Report struct {
	Sigma      float64   `json:"sigma"`     // global robust sigma (median tile)
	SigmaP90   float64   `json:"sigma_p90"` // 90th percentile of the per-tile sigmas
	Background float64   `json:"background"`
	SNR        float64   `json:"snr"`
	GridW      int       `json:"grid_w"`
	GridH      int       `json:"grid_h"`
	Tile       int       `json:"tile"`
	Tiles      []float32 `json:"-"` // per-tile sigma grid (GridW*GridH), for the heatmap
}

// Summary is the compact before/after record the pipeline persists for a denoise step. It is a data
// carrier for callers of this package; nothing here produces it directly.
type Summary struct {
	SigmaBefore float64   `json:"sigma_before"`
	SigmaAfter  float64   `json:"sigma_after,omitempty"`
	SNR         float64   `json:"snr,omitempty"`
	Background  float64   `json:"background,omitempty"`
	SirilSigma  []float64 `json:"siril_sigma,omitempty"`
}

// Measure estimates the noise of an image on plane 0 (luminance/mono). It is safe on multi-channel
// images (only channel 0 is read) and on degenerate input (returns a zeroed Report).
func Measure(im *fits.Image) Report {
	if im == nil || im.C < 1 || im.W <= 0 || im.H <= 0 || len(im.Pix) == 0 {
		return Report{Tile: tileSize}
	}
	return measurePlane(im.Pix[0], im.W, im.H)
}

// measurePlane runs the starlet measurement on a single plane.
func measurePlane(p []float32, w, h int) Report {
	cJ, wcoef := Decompose(p, w, h, measureScales)
	grid, gw, gh, sigmaG, p90 := tileSigmaGrid(wcoef[0], w, h)
	tiles := make([]float32, len(grid))
	for i, v := range grid {
		tiles[i] = float32(v)
	}
	return Report{
		Sigma:      sigmaG,
		SigmaP90:   p90,
		Background: planeBackground(cJ),
		SNR:        planeSNR(p, sigmaG),
		GridW:      gw,
		GridH:      gh,
		Tile:       tileSize,
		Tiles:      tiles,
	}
}

// tileSigmaGrid computes the per-tile robust noise sigma of detail plane w0 on a tileSize grid. It
// returns the grid clamped to [0.25,4]·sigmaG (float64), the grid dimensions, sigmaG (median of the
// raw per-tile sigmas) and p90 (their 90th percentile). Each tile sigma is 1.4826·MAD(w0)/starletSigma[0].
func tileSigmaGrid(w0 []float32, w, h int) (grid []float64, gw, gh int, sigmaG, p90 float64) {
	gw, gh = ceilDiv(w, tileSize), ceilDiv(h, tileSize)
	grid = make([]float64, gw*gh)
	var buf []float64
	for ty := 0; ty < gh; ty++ {
		for tx := 0; tx < gw; tx++ {
			buf = tileValues(w0, w, h, tileSize, tx, ty, buf[:0])
			grid[ty*gw+tx] = robustSigma(buf) / starletSigma[0]
		}
	}
	raw := copyOf(grid)
	sort.Float64s(raw)
	sigmaG = percentileSorted(raw, 50)
	p90 = percentileSorted(raw, 90)
	lo, hi := 0.25*sigmaG, 4*sigmaG
	for i := range grid {
		grid[i] = clamp(grid[i], lo, hi)
	}
	return grid, gw, gh, sigmaG, p90
}

// planeBackground estimates the background as the median of the coarsest smooth, computed on a capped
// subsample for speed (the smooth is heavily blurred, so the subsample median is essentially exact).
func planeBackground(cJ []float32) float64 {
	sub := subsample64(cJ, snrSubsampleMax)
	return median64(sub)
}

// planeSNR estimates a robust SNR as (P99.5 − median) / sigmaG using a capped subsample of the plane.
func planeSNR(p []float32, sigmaG float64) float64 {
	if !(sigmaG > 0) {
		return 0
	}
	sub := subsample64(p, snrSubsampleMax)
	sort.Float64s(sub)
	med := percentileSorted(sub, 50)
	hi := percentileSorted(sub, 99.5)
	return (hi - med) / sigmaG
}
