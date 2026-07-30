package planetary

import (
	"context"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Sharpness-weighted lucky-imaging stack. A plain (even winsorized) mean of the aligned frames trends
// toward the AVERAGE per-frame sharpness, so the many merely-good frames dilute the few lucky-sharp ones
// and the master ends up softer than the single sharpest frame. Weighting each frame by its sharpness —
// and keeping only a sharp subset upstream — makes the stack track the sharpest frames instead, so it
// rivals a single frame while still averaging down noise (which is what lets the deconvolution finish
// push past a single frame). Siril can't weight a starless stack, so we do it here in Go.
const (
	stackWeightPow = 3.0  // emphasise the sharpest frames (weight = (sharpness/maxSharpness)^pow)
	stackWeightMin = 0.02 // floor so a soft frame still trims noise a little, never fully dropped
	stackClipSigma = 2.5  // per-pixel sigma-clip: reject transients (satellites, cosmic rays)
	stackNormPct   = 0.999
	// With per-AP SELECTION (apSelectionFields) the local grids carry the discrimination, so the
	// global weight flattens to a mild preference (pow 1) — a steep global pow would shrink the
	// selected set's effective frame count for no sharpness gain — and the sigma-clip loosens to 4σ:
	// a tight clip is biased AGAINST the sharpest samples (at a crater rim the locally-sharpest
	// frames are exactly the extreme values relative to a soft-dominated mean), so it keeps only
	// true-transient rejection when selection is on.
	stackWeightPowSelected = 1.0
	stackClipSigmaSelected = 4.0
)

// stackWeightedFile writes a sharpness-weighted, sigma-clipped mean of the aligned frames to
// masterPath+".fits" (normalized to ~[0,1] like Siril's output_norm, so the existing finish is
// unchanged). scores are the frames' sharpness (higher = sharper); paths and scores are 1:1. Frames
// decode on parallel readers but accumulate in strict index order on one goroutine (two disk passes),
// so memory stays at a few accumulators + a small decode window and the sums are bit-identical to a
// serial loop.
func stackWeightedFile(ctx context.Context, paths []string, scores []float64, masterPath string) error {
	return stackWeightedFileAP(ctx, paths, scores, nil, masterPath, nil)
}

// stackWeightedFileAP is stackWeightedFile with optional per-frame MULTI-POINT quality weights
// (apFields, one apGridN×apGridN grid per frame, from apSelectionFields): each pixel's weight becomes
// globalFrameWeight × the bilinear interpolation of the frame's local-quality grid — so every region
// of the master is dominated by the frames that were sharpest THERE (a globally-good frame with one
// seeing-smeared quadrant contributes everywhere else, not in the smear). nil apFields = global only.
// onFrame (nil-safe) ticks once per accumulated frame across both passes for live progress.
func stackWeightedFileAP(ctx context.Context, paths []string, scores []float64, apFields [][]float64,
	masterPath string, onFrame func(done, total int)) error {
	if len(paths) == 0 {
		return fmt.Errorf("no frames to stack")
	}
	if apFields != nil && len(apFields) != len(paths) {
		return fmt.Errorf("stack: %d AP weight fields for %d frames", len(apFields), len(paths))
	}
	first, err := fits.ReadImage(paths[0])
	if err != nil {
		return fmt.Errorf("stack: read %s: %w", paths[0], err)
	}
	pow, clip := stackWeightPow, stackClipSigma
	if apFields != nil {
		pow, clip = stackWeightPowSelected, stackClipSigmaSelected
	}
	weights := sharpnessWeights(scores, pow)
	total := 2 * len(paths) // two passes
	tick := func(done int) {
		if onFrame != nil {
			onFrame(done, total)
		}
	}
	mean, std, err := weightedMoments(ctx, paths, weights, apFields, first.W, first.H, first.C, tick)
	if err != nil {
		return err
	}
	master, err := clippedWeightedMean(ctx, paths, weights, apFields, mean, std, clip, first.W, first.H, first.C, tick)
	if err != nil {
		return err
	}
	normalize(master, stackNormPct)
	return master.WriteFITS(masterPath + ".fits")
}

// sharpnessWeights maps sharpness scores to stacking weights = (s/max)^pow, floored so no kept
// frame is fully discarded. A zero/empty score set yields uniform weights.
func sharpnessWeights(scores []float64, pow float64) []float64 {
	maxS := 0.0
	for _, s := range scores {
		if s > maxS {
			maxS = s
		}
	}
	w := make([]float64, len(scores))
	for i, s := range scores {
		if maxS <= 0 {
			w[i] = 1
			continue
		}
		ws := math.Pow(s/maxS, pow)
		if ws < stackWeightMin {
			ws = stackWeightMin
		}
		w[i] = ws
	}
	return w
}

// weightedMoments accumulates the per-pixel weighted mean and standard deviation over all frames
// (pass 1). Decoding is parallel; accumulation runs in strict frame order on the consumer goroutine,
// which also owns the reused wplane buffer (a nil/mismatched frame skips exactly like before).
func weightedMoments(ctx context.Context, paths []string, weights []float64, apFields [][]float64,
	w, h, c int, tick func(done int)) (mean, std [][]float64, err error) {
	n := w * h
	sum := newPlanes(c, n)
	wsum := newPlanes(c, n)
	sumSq := newPlanes(c, n)
	wplane := make([]float64, n) // reused per-frame pixel-weight buffer, consumer-owned
	done := 0
	err = orderedFrames(ctx, paths, planetaryWorkers(), func(i int, im *fits.Image) {
		done++
		tick(done)
		if im == nil || im.W != w || im.H != h || im.C != c {
			return
		}
		pixelWeights(wplane, weights[i], frameField(apFields, i), w, h)
		accumulate(sum, wsum, sumSq, im, wplane)
	})
	if err != nil {
		return nil, nil, err
	}
	mean = newPlanes(c, n)
	std = newPlanes(c, n)
	for ch := 0; ch < c; ch++ {
		for j := 0; j < n; j++ {
			if wsum[ch][j] <= 0 {
				continue
			}
			m := sum[ch][j] / wsum[ch][j]
			mean[ch][j] = m
			if v := sumSq[ch][j]/wsum[ch][j] - m*m; v > 0 {
				std[ch][j] = math.Sqrt(v)
			}
		}
	}
	return mean, std, nil
}

// frameField returns frame i's AP weight grid, or nil when multi-point weighting is off.
func frameField(apFields [][]float64, i int) []float64 {
	if apFields == nil {
		return nil
	}
	return apFields[i]
}

// pixelWeights fills dst with the frame's per-pixel stacking weight: the global frame weight,
// modulated by the bilinear interpolation of its AP selection grid when present.
func pixelWeights(dst []float64, frameW float64, field []float64, w, h int) {
	if field == nil {
		for j := range dst {
			dst[j] = frameW
		}
		return
	}
	n := gridSize(field)
	for y := 0; y < h; y++ {
		row := y * w
		for x := 0; x < w; x++ {
			dst[row+x] = frameW * sampleWeightField(field, w, h, x, y, n)
		}
	}
}

// accumulate adds one frame into the running sum / weight / sum-of-squares planes with per-pixel weights.
func accumulate(sum, wsum, sumSq [][]float64, im *fits.Image, wplane []float64) {
	for ch := 0; ch < im.C; ch++ {
		px := im.Pix[ch]
		s, ws, sq := sum[ch], wsum[ch], sumSq[ch]
		for j := range px {
			v, wt := float64(px[j]), wplane[j]
			s[j] += wt * v
			ws[j] += wt
			sq[j] += wt * v * v
		}
	}
}

// clippedWeightedMean re-accumulates the weighted mean over pass 2, rejecting per-pixel outliers beyond
// clipSigma of the pass-1 mean (transients). Pixels with no surviving samples fall back to the mean.
// Same parallel-decode / in-order-accumulate shape (and identical skip semantics) as pass 1.
func clippedWeightedMean(ctx context.Context, paths []string, weights []float64, apFields [][]float64,
	mean, std [][]float64, clipSigma float64, w, h, c int, tick func(done int)) (*fits.Image, error) {
	n := w * h
	csum := newPlanes(c, n)
	cw := newPlanes(c, n)
	wplane := make([]float64, n)
	done := len(paths) // pass-2 ticks continue after pass 1's
	err := orderedFrames(ctx, paths, planetaryWorkers(), func(i int, im *fits.Image) {
		done++
		tick(done)
		if im == nil || im.W != w || im.H != h || im.C != c {
			return
		}
		pixelWeights(wplane, weights[i], frameField(apFields, i), w, h)
		for ch := 0; ch < c; ch++ {
			px := im.Pix[ch]
			for j := range px {
				v := float64(px[j])
				if std[ch][j] == 0 || math.Abs(v-mean[ch][j]) <= clipSigma*std[ch][j] {
					csum[ch][j] += wplane[j] * v
					cw[ch][j] += wplane[j]
				}
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out := fits.NewImage(w, h, c)
	for ch := 0; ch < c; ch++ {
		for j := 0; j < n; j++ {
			if cw[ch][j] > 0 {
				out.Pix[ch][j] = float32(csum[ch][j] / cw[ch][j])
			} else {
				out.Pix[ch][j] = float32(mean[ch][j])
			}
		}
	}
	return out, nil
}

// normalize rescales all channels so the given high percentile of the luminance maps to 1.0 (matching
// Siril's output_norm, so the downstream deconv/finish sees the same range it did before).
func normalize(im *fits.Image, pct float64) {
	hi := planePercentile(im.Pix[0], pct)
	if hi <= 0 {
		return
	}
	inv := float32(1.0 / hi)
	for ch := 0; ch < im.C; ch++ {
		for j := range im.Pix[ch] {
			im.Pix[ch][j] *= inv
		}
	}
}

func newPlanes(c, n int) [][]float64 {
	p := make([][]float64, c)
	for ch := range p {
		p[ch] = make([]float64, n)
	}
	return p
}

// planePercentile returns the p-quantile (0..1) of a plane via a capped sample (fast, robust to size).
func planePercentile(v []float32, p float64) float64 {
	return lowPercentile(v, p)
}
