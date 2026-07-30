package pipeline

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/noise"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/transient"
)

// A multi-night master's per-pixel stack depth varies across the canvas (5 nights in the core, 1–2
// in the rotated margins), so the noise floor STEPS at every coverage boundary — the grain
// component of the seam cut-lines. The equalization runs one coverage-weighted starlet pass whose
// per-pixel strength is exactly the excess noise factor √(depth_max/depth)−1: zero (byte-identical)
// in the full-depth core, ramping smoothly through blurred coverage so no new edge is introduced.
const (
	// seamNoiseMaxWeight caps the per-pixel strength — never harder than a heavy manual starlet.
	seamNoiseMaxWeight = 2.5
	// seamNoiseBlurCells is the Gaussian sigma (in grid cells; ×Scale px ≈ a 96 px ramp) blurring
	// the coverage before the weight map — the fade that prevents the correction stepping.
	seamNoiseBlurCells = 12.0
	// seamNoiseMinDepthRatio: below this max/min depth ratio the noise step is invisible — skip.
	seamNoiseMinDepthRatio = 1.3
)

// SeamNoiseEq records one channel's coverage-weighted noise equalization outcome (run.json
// provenance; the pass itself fades the noise-depth step at coverage boundaries).
type SeamNoiseEq struct {
	DepthMin    int     `json:"depth_min"`
	DepthMax    int     `json:"depth_max"`
	WeightMax   float64 `json:"weight_max"`
	SigmaBefore float64 `json:"sigma_before"`
	SigmaAfter  float64 `json:"sigma_after"`
	Applied     bool    `json:"applied"`
	Reason      string  `json:"reason,omitempty"`
}

// equalizeSeamNoise runs the weighted starlet pass on the linear master (in place), recording the
// outcome on ch.Seam.NoiseEq. Soft-fail throughout: any guard or error leaves the master untouched
// with a reason.
func equalizeSeamNoise(ctx context.Context, opts Options, ch *ChannelResult, masterName, outDir, filter string,
	onProgress func(siril.Progress)) {
	eq := &SeamNoiseEq{}
	if ch.Seam == nil {
		ch.Seam = &SeamRepair{}
	}
	ch.Seam.NoiseEq = eq
	if ctx.Err() != nil {
		eq.Reason = "cancelled"
		return
	}
	path := ch.OutputPath
	im, err := fits.ReadImage(path)
	if err != nil {
		eq.Reason = "read master: " + err.Error()
		return
	}
	if need := int64(im.W) * int64(im.H) * 4 * 10; need > transient.MemBudget() {
		eq.Reason = fmt.Sprintf("skipped: needs ~%d MB, over the memory budget", need>>20)
		warnChannel(opts, ch, filter+": seam noise equalization "+eq.Reason)
		return
	}
	weights, reason := seamNoiseWeights(ch.coverage, im.W, im.H)
	if reason != "" {
		eq.Reason = reason
		return
	}
	fillNoiseEqDepths(eq, ch.coverage, weights)
	start := time.Now()
	emit(onProgress, fmt.Sprintf("▶ seam noise equalization %s (depth %d→%d, weight ≤%.2f)",
		filter, eq.DepthMin, eq.DepthMax, eq.WeightMax))
	eq.SigmaBefore = noise.Measure(im).Sigma
	if err := noise.DenoiseWeighted(im, noise.DefaultOptions(), weights); err != nil {
		eq.Reason = "weighted denoise: " + err.Error()
		return
	}
	if err := im.OverwriteData(path); err != nil {
		eq.Reason = "rewrite master: " + err.Error()
		return
	}
	eq.SigmaAfter = noise.Measure(im).Sigma
	eq.Applied = true
	emit(onProgress, fmt.Sprintf("✓ seam noise equalization %s done in %s (σ %.4g → %.4g)",
		filter, time.Since(start).Round(time.Second), eq.SigmaBefore, eq.SigmaAfter))
}

// seamNoiseWeights maps the coverage grid to a full-resolution starlet weight plane: counts are
// smoothed by NORMALIZED CONVOLUTION over covered cells only (blur(count·covered)/blur(covered) —
// the nightscape drift-edge pattern, so the ramp never averages in the empty outside), then
// w = clamp(√(cMax/c̃)−1, 0, seamNoiseMaxWeight), bilinearly upsampled. Never-covered cells stay
// exactly 0. A non-empty reason means the pass should be skipped.
func seamNoiseWeights(g *coverageGrid, w, h int) ([]float32, string) {
	if g == nil || len(g.Counts) == 0 {
		return nil, "no coverage grid"
	}
	if g.W != (w+g.Scale-1)/g.Scale || g.H != (h+g.Scale-1)/g.Scale {
		return nil, "coverage grid does not match the master canvas"
	}
	cMax, cMin := uint16(0), uint16(math.MaxUint16)
	for _, c := range g.Counts {
		if c == 0 {
			continue
		}
		if c > cMax {
			cMax = c
		}
		if c < cMin {
			cMin = c
		}
	}
	if cMax == 0 {
		return nil, "no covered cells"
	}
	if float64(cMax)/float64(cMin) < seamNoiseMinDepthRatio {
		return nil, fmt.Sprintf("stack depth nearly uniform (%d→%d) — no visible noise step", cMin, cMax)
	}
	vals := make([]float32, len(g.Counts))
	cov := make([]float32, len(g.Counts))
	for i, c := range g.Counts {
		if c > 0 {
			vals[i] = float32(c)
			cov[i] = 1
		}
	}
	blurV := imgops.GaussianBlur(vals, g.W, g.H, seamNoiseBlurCells)
	blurC := imgops.GaussianBlur(cov, g.W, g.H, seamNoiseBlurCells)
	grid := make([]float32, len(g.Counts))
	for i := range grid {
		if g.Counts[i] == 0 || blurC[i] <= 1e-6 {
			continue // never-covered stays 0 — the mosaic fill owns those pixels
		}
		smooth := float64(blurV[i] / blurC[i])
		if smooth < 1 {
			smooth = 1
		}
		wgt := math.Sqrt(float64(cMax)/smooth) - 1
		grid[i] = float32(math.Min(math.Max(wgt, 0), seamNoiseMaxWeight))
	}
	return upsampleWeightGrid(grid, g, w, h), ""
}

// upsampleWeightGrid bilinearly upsamples the cell grid to per-pixel weights, forcing pixels whose
// RAW cell is uncovered to exactly 0 (the bilinear ramp must not bleed into never-covered sky).
func upsampleWeightGrid(grid []float32, g *coverageGrid, w, h int) []float32 {
	out := make([]float32, w*h)
	scale := float64(g.Scale)
	for y := 0; y < h; y++ {
		gy := (float64(y)+0.5)/scale - 0.5
		row := y * w
		for x := 0; x < w; x++ {
			cell := min(y/g.Scale, g.H-1)*g.W + min(x/g.Scale, g.W-1)
			if g.Counts[cell] == 0 {
				continue
			}
			gx := (float64(x)+0.5)/scale - 0.5
			out[row+x] = bilinearCell(grid, g.W, g.H, gx, gy)
		}
	}
	return out
}

// bilinearCell samples a cell grid at fractional coordinates with edge clamping.
func bilinearCell(grid []float32, gw, gh int, x, y float64) float32 {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	fx := x - float64(x0)
	fy := y - float64(y0)
	c := func(cx, cy int) float64 {
		cx = min(max(cx, 0), gw-1)
		cy = min(max(cy, 0), gh-1)
		return float64(grid[cy*gw+cx])
	}
	top := c(x0, y0)*(1-fx) + c(x0+1, y0)*fx
	bot := c(x0, y0+1)*(1-fx) + c(x0+1, y0+1)*fx
	return float32(top*(1-fy) + bot*fy)
}

// fillNoiseEqDepths records the depth range + peak weight for the run record.
func fillNoiseEqDepths(eq *SeamNoiseEq, g *coverageGrid, weights []float32) {
	cMax, cMin := 0, math.MaxInt32
	for _, c := range g.Counts {
		if c == 0 {
			continue
		}
		if int(c) > cMax {
			cMax = int(c)
		}
		if int(c) < cMin {
			cMin = int(c)
		}
	}
	eq.DepthMin, eq.DepthMax = cMin, cMax
	peak := float32(0)
	for _, w := range weights {
		if w > peak {
			peak = w
		}
	}
	eq.WeightMax = float64(peak)
}

// emit forwards a journal line through the channel's progress sink (nil-safe).
func emit(onProgress func(siril.Progress), line string) {
	if onProgress != nil {
		onProgress(siril.Progress{Line: line})
	}
}
