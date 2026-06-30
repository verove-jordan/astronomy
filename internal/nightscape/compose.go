package nightscape

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// buildSkyAlpha builds a soft sky/foreground alpha (1 = sky, 0 = foreground) from the LINEAR
// foreground frame. Sky candidates are pixels above a log-luminance percentile; spatial coherence
// comes from flood-filling the connected components that touch the brighter image edge (the sky),
// so bright gaps between leaves are not mistaken for sky. The foreground is then dilated for a safe
// margin and the edge feathered. (main.py build_sky_alpha_from_linear_fg)
func buildSkyAlpha(fg *fits.Image, fgPct float64, dilation int, blurSigma float64) []float32 {
	lum := luminance(fg)
	w, h := fg.W, fg.H

	logLum := make([]float32, len(lum))
	for i, v := range lum {
		if v < 1e-10 {
			v = 1e-10
		}
		logLum[i] = float32(math.Log(float64(v)))
	}
	threshold := float32(math.Exp(percentile(logLum, fgPct)))

	cand := make([]bool, len(lum))
	for i := range lum {
		cand[i] = lum[i] >= threshold
	}
	labels, _ := label(cand, w, h)

	// Seed from the brighter of the top/bottom edge rows — that edge is the sky, whichever way the
	// frame is oriented.
	seedRow := 0
	if rowMean(lum, w, h-1) > rowMean(lum, w, 0) {
		seedRow = h - 1
	}
	seedLabels := map[int]bool{}
	for x := 0; x < w; x++ {
		if l := labels[seedRow*w+x]; l != 0 {
			seedLabels[l] = true
		}
	}

	trees := make([]bool, len(lum))
	if len(seedLabels) == 0 {
		for i := range cand {
			trees[i] = !cand[i] // fallback: raw threshold
		}
	} else {
		for i, l := range labels {
			trees[i] = !seedLabels[l]
		}
	}

	if dilation > 0 {
		trees = binaryDilation(trees, w, h, dilation)
	}
	alpha := make([]float32, len(trees))
	for i := range trees {
		if !trees[i] {
			alpha[i] = 1
		}
	}
	if blurSigma > 0 {
		alpha = gaussianBlur(alpha, w, h, blurSigma)
	}
	return alpha
}

func rowMean(grid []float32, w, y int) float64 {
	sum := 0.0
	base := y * w
	for x := 0; x < w; x++ {
		sum += float64(grid[base+x])
	}
	return sum / float64(w)
}

// computeCleanSkyStack combines, per pixel, only the frames where that pixel is sky — pixels above a
// per-frame luminance percentile, optionally excluding green-dominant foliage — using a SIGMA-CLIPPED
// (winsorized) mean: a first pass measures each pixel's luminance mean+σ over its sky frames, then a
// second pass averages only the frames within ~2.5σ, so hot pixels, satellites, plane trails and noise
// spikes are rejected before averaging (the smoothness the reference's Siril `stack rej 3 3` provides,
// which a plain mean lacked → grain). The sky-pixel selection still removes drifting-tree ghosts. Pixels
// with no sky frame fall back to the foreground (masked out in the composite). Frames are streamed twice
// (one at a time) to bound memory; the per-frame normalization reference is computed once and reused.
// (main.py compute_clean_sky_stack)
func computeCleanSkyStack(alignedPaths []string, fg *fits.Image, skyPct float64, rejectGreen bool, greenRatio, minCoverageFrac float64, prep func(*fits.Image)) (*fits.Image, error) {
	if len(alignedPaths) == 0 {
		return nil, fmt.Errorf("no aligned frames to stack")
	}
	var (
		w, h, c int
		ref     frameNorm
		haveRef bool
	)
	// streamFrames reads → preps → normalizes each frame (the per-channel bg+gain match to the first frame,
	// so auto-ISO/WB drift doesn't muddy colour and the sky threshold is comparable) and invokes perFrame.
	// Both passes use it, so the normalization reference is computed once and reused.
	streamFrames := func(perFrame func(frame *fits.Image, idx int)) error {
		for idx, path := range alignedPaths {
			frame, err := fits.ReadImage(path)
			if err != nil {
				return fmt.Errorf("read aligned frame %s: %w", path, err)
			}
			if prep != nil {
				prep(frame)
			}
			fn := measureFrame(frame)
			if !haveRef {
				ref, haveRef = fn, true
			}
			normalizeToRef(frame, fn, ref)
			if w == 0 {
				w, h, c = frame.W, frame.H, frame.C
			} else if frame.W != w || frame.H != h || frame.C != c {
				return fmt.Errorf("aligned frame %s size %dx%dx%d != %dx%dx%d", path, frame.W, frame.H, frame.C, w, h, c)
			}
			perFrame(frame, idx)
		}
		return nil
	}
	frameLum := func(frame *fits.Image) []float32 {
		if frame.C == 3 {
			return frame.Pix[1] // green channel, as in the reference recipe
		}
		return frame.Pix[0]
	}
	isSky := func(frame *fits.Image, lum []float32, threshold float32, i int) bool {
		if lum[i] < threshold {
			return false
		}
		if rejectGreen && frame.C == 3 {
			maxRB := frame.Pix[0][i]
			if frame.Pix[2][i] > maxRB {
				maxRB = frame.Pix[2][i]
			}
			if frame.Pix[1][i] > float32(greenRatio)*maxRB {
				return false
			}
		}
		return true
	}

	// Pass 1: per-pixel luminance mean + variance over the selected sky frames (drives the σ-clip below).
	thresholds := make([]float32, len(alignedPaths))
	var sumL, sumL2, cnt1 []float64
	if err := streamFrames(func(frame *fits.Image, idx int) {
		if sumL == nil {
			sumL, sumL2, cnt1 = make([]float64, w*h), make([]float64, w*h), make([]float64, w*h)
		}
		lum := frameLum(frame)
		threshold := float32(percentile(lum, skyPct))
		thresholds[idx] = threshold
		for i := range lum {
			if !isSky(frame, lum, threshold, i) {
				continue
			}
			v := float64(lum[i])
			sumL[i] += v
			sumL2[i] += v * v
			cnt1[i]++
		}
	}); err != nil {
		return nil, err
	}

	// Per-pixel σ-clip bounds. Pixels with <3 sky frames can't estimate σ → accept all (no clip).
	const kSigma = 2.5
	loL, hiL := make([]float64, w*h), make([]float64, w*h)
	for i := range loL {
		if cnt1[i] < 3 {
			loL[i], hiL[i] = math.Inf(-1), math.Inf(1)
			continue
		}
		m := sumL[i] / cnt1[i]
		varr := sumL2[i]/cnt1[i] - m*m
		if varr < 0 {
			varr = 0
		}
		sd := kSigma * math.Sqrt(varr)
		loL[i], hiL[i] = m-sd, m+sd
	}

	// Pass 2: clipped per-channel mean — accumulate only the frames whose luminance is within k·σ.
	var accum [][]float64
	var counts []float64
	if err := streamFrames(func(frame *fits.Image, idx int) {
		if accum == nil {
			accum = make([][]float64, c)
			for ch := range accum {
				accum[ch] = make([]float64, w*h)
			}
			counts = make([]float64, w*h)
		}
		lum := frameLum(frame)
		threshold := thresholds[idx]
		for i := range lum {
			if !isSky(frame, lum, threshold, i) {
				continue
			}
			if v := float64(lum[i]); v < loL[i] || v > hiL[i] {
				continue // σ-clipped outlier (hot pixel, satellite, spike)
			}
			for ch := 0; ch < c; ch++ {
				accum[ch][i] += float64(frame.Pix[ch][i])
			}
			counts[i]++
		}
	}); err != nil {
		return nil, err
	}

	// Reject low-coverage pixels: where the untracked sky has drifted out of frame, only a few frames
	// cover a pixel and their bright light-pollution/amp-glow averages into a blown edge band that
	// would dominate the stretch. Such pixels are replaced by the reliable sky background (per-channel
	// median of well-covered pixels), so the edges read as dark sky instead of a magenta strip.
	minCount := int(math.Round(minCoverageFrac * float64(len(alignedPaths))))
	if minCount < 1 {
		minCount = 1
	}
	out := fits.NewImage(w, h, c)
	reliable := make([]bool, w*h)
	for i := 0; i < w*h; i++ {
		if int(counts[i]) >= minCount {
			reliable[i] = true
			for ch := 0; ch < c; ch++ {
				out.Pix[ch][i] = float32(accum[ch][i] / counts[i])
			}
		}
	}
	// Fill the low-coverage pixels by EXTRAPOLATING the reliable sky (normalized convolution: blur the
	// reliable values and the reliable mask, then divide) so the uncovered drift edges blend smoothly
	// into the local sky and carry its own gradient — instead of a flat percentile block that reads as a
	// visibly different band. A wide blur reaches across the gap; a dark-sky floor backs the deep interior
	// where even the blur has no support.
	relMask := make([]float32, w*h)
	for i := range relMask {
		if reliable[i] {
			relMask[i] = 1
		}
	}
	sigma := float64(max(w, h)) / 6 // wide blend so the drift edge fades smoothly into the local sky
	wBlur := gaussianBlur(relMask, w, h, sigma)
	weighted := make([]float32, w*h)
	for ch := 0; ch < c; ch++ {
		p := out.Pix[ch]
		for i := range p {
			weighted[i] = p[i] // 0 outside the reliable region (out was zero-initialised)
		}
		fill := gaussianBlur(weighted, w, h, sigma)
		vals := make([]float32, 0, w*h)
		for i := 0; i < w*h; i++ {
			if reliable[i] {
				vals = append(vals, p[i])
			}
		}
		floor := float32(percentile(vals, 15))
		for i := 0; i < w*h; i++ {
			if reliable[i] {
				continue
			}
			if wBlur[i] > 1e-3 {
				p[i] = fill[i] / wBlur[i]
			} else {
				p[i] = floor
			}
		}
	}
	return out, nil
}

// compositeLayers blends the stacked sky and the single foreground through the sky alpha:
// result = alpha*sky + (1-alpha)*fg. Both must share pixel coordinates (guaranteed by registering
// with the foreground frame as the reference). (main.py composite_layers, alpha path)
func compositeLayers(sky, fg *fits.Image, alpha []float32) (*fits.Image, error) {
	if sky.W != fg.W || sky.H != fg.H || sky.C != fg.C {
		return nil, fmt.Errorf("composite size mismatch sky %dx%dx%d vs fg %dx%dx%d", sky.W, sky.H, sky.C, fg.W, fg.H, fg.C)
	}
	out := fits.NewImage(sky.W, sky.H, sky.C)
	for c := 0; c < sky.C; c++ {
		s, f, o := sky.Pix[c], fg.Pix[c], out.Pix[c]
		for i := range o {
			a := alpha[i]
			o[i] = a*s[i] + (1-a)*f[i]
		}
	}
	return out, nil
}

// cleanHotPixels replaces per-channel outliers (|pixel - local median| > sigma*std) with the local
// median — the single unaligned foreground frame keeps the sensor's hot pixels that a stack would
// have rejected. (main.py clean_hot_pixels)
func cleanHotPixels(im *fits.Image, sigma float64) {
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		med := medianFilter(p, im.W, im.H, 3)
		var mean, m2 float64
		for i := range p {
			mean += float64(p[i] - med[i])
		}
		mean /= float64(len(p))
		for i := range p {
			d := float64(p[i]-med[i]) - mean
			m2 += d * d
		}
		std := math.Sqrt(m2 / float64(len(p)))
		thresh := float32(sigma * std)
		for i := range p {
			if d := p[i] - med[i]; d > thresh || d < -thresh {
				p[i] = med[i]
			}
		}
	}
}
