package nightscape

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// maskPreBlurFrac smooths the luminance before it is thresholded, as a fraction of the frame's long
// edge. Without it the threshold cuts through per-pixel noise and the candidate mask shatters: a
// real 4032x3024 zenith frame produced 22,994 connected components, so the flood fill from the seed
// row reached 21 specks and declared the whole frame foreground. The sky/ground split is a
// large-scale decision and has to be made on large-scale structure.
const maskPreBlurFrac = 0.004

// minSkyFrac is the sky fraction below which the mask is not believed. A frame that reached this
// code was stacked as sky, so a mask claiming it is essentially all ground has failed, not
// discovered something. Falling back to all-sky shows the stack; trusting it shows one raw frame.
const minSkyFrac = 0.05

// darkFloorFrac is the share of a frame's own median luminance below which a pixel is taken to be
// something the camera was looking past rather than sky. Measured on a real zenith panel the dimmest
// sky sat at 84% of the median while an obstruction sits at a few percent, so half the median falls
// in a wide empty gap between the two populations.
const darkFloorFrac = 0.5

// darkFloorSamples bounds the subsample the floor is estimated from.
const darkFloorSamples = 300000

// buildSkyAlpha builds a soft sky/foreground alpha (1 = sky, 0 = foreground) from the LINEAR
// foreground frame. Sky candidates are pixels above a log-luminance percentile of the SMOOTHED
// luminance; spatial coherence comes from flood-filling the connected components that touch the
// brighter image edge (the sky), so bright gaps between leaves are not mistaken for sky. The
// foreground is then dilated for a safe margin and the edge feathered.
// (main.py build_sky_alpha_from_linear_fg)
//
// It returns a note when it fell back to all-sky, so a run says why rather than quietly producing a
// composite of the wrong layer.
func buildSkyAlpha(fg *fits.Image, fgPct float64, dilation int, blurSigma float64, prior []bool) ([]float32, string) {
	lum := luminance(fg)
	w, h := fg.W, fg.H

	if sigma := maskPreBlurFrac * float64(max(w, h)); sigma >= 1 {
		lum = gaussianBlur(lum, w, h, sigma)
	}
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

	trees := make([]bool, len(lum))
	note := ""
	if len(prior) == len(lum) {
		// The geometry says roughly where the ground is; the picture says exactly.
		//
		// The prior alone is a straight line, and a straight line is not where a horizon is. Measured
		// on this session it left a strip of real dark shoreline on the SKY side, which the landscape
		// layer then never painted — the dark band between the sea and the sky. refineHorizon snaps it
		// onto the edge the frame actually has, and falls back to the prior line for line wherever
		// there is no convincing edge to snap to.
		//
		// Growing the mask into dark shapes that touch it was tried and removed: below the threshold
		// the dark sky and the dark ground are ONE connected region joined across the horizon, so the
		// flood swallowed the entire sky in a single step. Anything standing above the horizon line —
		// a tree, a mast — is therefore still not carved out here. It does not end up in the stack
		// either, because the dark floor refuses it pixel by pixel.
		if r, moved, ok := refineHorizon(prior, lum, w, h); ok {
			copy(trees, r)
			note = fmt.Sprintf("horizon snapped to the frame's own edge, %.0f px from the pointing's line", moved)
		} else {
			copy(trees, prior)
		}
	} else {
		// No pointing to go on: fall back to flood-filling the sky from the brightest of the four
		// image edges. Considering all four matters because a phone held in portrait stores its
		// pixels landscape, so its horizon runs DOWN the frame this code sees and neither the top
		// nor the bottom row is the sky edge.
		seedLabels := map[int]bool{}
		for _, l := range edgeLabels(labels, lum, w, h) {
			seedLabels[l] = true
		}
		if len(seedLabels) == 0 {
			for i := range cand {
				trees[i] = !cand[i] // fallback: raw threshold
			}
		} else {
			for i, l := range labels {
				trees[i] = !seedLabels[l]
			}
		}
	}

	// The dilation is NOT just a safety margin for a boundary that might be misplaced, and shrinking
	// it once the boundary was fitted to the picture made the canvas measurably worse: the global
	// colour cast went up 5.5x and the spatial colour drift 1.6x.
	//
	// The reason is that this alpha has four consumers, not one. It is the composite's mask, but it is
	// ALSO the sky mask that removeSkyGradient fits its background model through and that autoStretch
	// measures the sky level with. The dilation is what keeps those two statistics away from the
	// horizon transition; take it away and the horizon glow lands in both. A tight mask for
	// compositing and a generous one for measuring would need to be two masks, and they are one.
	if dilation > 0 {
		trees = binaryDilation(trees, w, h, dilation)
	}
	alpha := make([]float32, len(trees))
	sky := 0
	for i := range trees {
		if !trees[i] {
			alpha[i] = 1
			sky++
		}
	}
	if float64(sky)/float64(len(alpha)) < minSkyFrac {
		return allSky(len(alpha)), "sky mask found no sky; composited the stack directly (no foreground in frame?)"
	}
	if blurSigma > 0 {
		alpha = gaussianBlur(alpha, w, h, blurSigma)
	}
	return alpha, note
}

// allSky is the alpha for a frame with no foreground at all: show the stacked sky everywhere.
func allSky(n int) []float32 {
	alpha := make([]float32, n)
	for i := range alpha {
		alpha[i] = 1
	}
	return alpha
}

// edgeLabels returns the component labels touching the brightest of the four image edges, skipping
// label 0 (the sub-threshold pixels, which are never sky).
func edgeLabels(labels []int, lum []float32, w, h int) []int {
	edges := []struct {
		mean  float64
		index func(k int) int
		n     int
	}{
		{rowMean(lum, w, 0), func(k int) int { return k }, w},
		{rowMean(lum, w, h-1), func(k int) int { return (h-1)*w + k }, w},
		{colMean(lum, w, h, 0), func(k int) int { return k * w }, h},
		{colMean(lum, w, h, w-1), func(k int) int { return k*w + w - 1 }, h},
	}
	best := 0
	for i, e := range edges {
		if e.mean > edges[best].mean {
			best = i
		}
	}
	var out []int
	for k := 0; k < edges[best].n; k++ {
		if l := labels[edges[best].index(k)]; l != 0 {
			out = append(out, l)
		}
	}
	return out
}

func colMean(grid []float32, w, h, x int) float64 {
	sum := 0.0
	for y := 0; y < h; y++ {
		sum += float64(grid[y*w+x])
	}
	return sum / float64(h)
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
// It also returns the per-pixel frame count, so callers can crop away the thin drift edges before
// measuring anything from the result.
func computeCleanSkyStack(alignedPaths []string, fg *fits.Image, skyPct float64, rejectGreen bool, greenRatio, minCoverageFrac float64, prep func(*fits.Image)) (*fits.Image, []float64, *Transients, error) {
	if len(alignedPaths) == 0 {
		return nil, nil, nil, fmt.Errorf("no aligned frames to stack")
	}
	var (
		w, h, c int
		ref     frameNorm
		haveRef bool
	)
	// streamFrames reads → preps → normalizes each frame (the per-channel bg+gain match to the first frame,
	// so auto-ISO/WB drift doesn't muddy colour and the sky threshold is comparable) and invokes perFrame.
	// Both passes use it, so the normalization reference is computed once and reused.
	// reached is captured BEFORE normalization. Registration zero-fills wherever a frame did not
	// reach, but normalization subtracts each frame's own background, after which those zeros are no
	// longer zero — and genuinely dark ground lands below zero alongside them. Asking "did this
	// frame reach this pixel" afterwards therefore cannot tell missing data from dark scenery, which
	// is what cropped a landscape panel down to its sky band.
	var reached []bool
	streamFrames := func(perFrame func(frame *fits.Image, idx int)) error {
		for idx, path := range alignedPaths {
			frame, err := fits.ReadImage(path)
			if err != nil {
				return fmt.Errorf("read aligned frame %s: %w", path, err)
			}
			if prep != nil {
				prep(frame)
			}
			if len(reached) != len(frame.Pix[0]) {
				reached = make([]bool, len(frame.Pix[0]))
			}
			for i := range reached {
				reached[i] = false
				for ch := 0; ch < frame.C; ch++ {
					if frame.Pix[ch][i] != 0 {
						reached[i] = true
						break
					}
				}
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
	isSky := func(frame *fits.Image, lum []float32, threshold, darkFloor float32, i int) bool {
		// A registered frame is zero-filled wherever it did not reach. That is "no data", not "very
		// dark sky": averaging it in would pull down every border the drift swept across. The
		// percentile threshold used to hide this by rejecting the darkest pixels anyway, which stops
		// being true the moment a frame with no foreground asks for every pixel.
		if !reached[i] {
			return false
		}
		// Anything far below the frame's OWN sky level is something the camera was looking past — a
		// rock, a bag, the edge of a roof. Rejecting on that instead of on a fixed share of the
		// pixels is what lets a frame take every pixel of sky and still refuse the obstruction in its
		// corner, and it is now the ONLY obstruction test: the percentile threshold below is passed 0
		// on both paths, because a share of the pixels cannot tell dark sky from ground. See the
		// measurement in nightscape.compose.
		if lum[i] < darkFloor {
			return false
		}
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
	darkFloors := make([]float32, len(alignedPaths))
	var sumL, sumL2, cnt1 []float64
	if err := streamFrames(func(frame *fits.Image, idx int) {
		if sumL == nil {
			sumL, sumL2, cnt1 = make([]float64, w*h), make([]float64, w*h), make([]float64, w*h)
		}
		lum := frameLum(frame)
		threshold := float32(percentile(lum, skyPct))
		thresholds[idx] = threshold
		// The floor comes off a subsample: it only has to place a cut between real sky and an
		// obstruction, which are orders of magnitude apart, and the full-array percentile above is
		// left untouched so the landscape path stays bit-for-bit what it was.
		darkFloor := float32(darkFloorFrac * percentile(subsample(lum, darkFloorSamples), 50))
		darkFloors[idx] = darkFloor
		for i := range lum {
			if !isSky(frame, lum, threshold, darkFloor, i) {
				continue
			}
			v := float64(lum[i])
			sumL[i] += v
			sumL2[i] += v * v
			cnt1[i]++
		}
	}); err != nil {
		return nil, nil, nil, err
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
	// What the clip throws away on the HIGH side is kept: that is where every meteor in the session
	// went, and a stack that silently deletes them is not the only thing anyone wants from the frames.
	tran := newTransients(w, h, c)
	var accum [][]float64
	var counts, covered []float64
	if err := streamFrames(func(frame *fits.Image, idx int) {
		if accum == nil {
			accum = make([][]float64, c)
			for ch := range accum {
				accum[ch] = make([]float64, w*h)
			}
			counts = make([]float64, w*h)
			covered = make([]float64, w*h)
		}
		lum := frameLum(frame)
		threshold, darkFloor := thresholds[idx], darkFloors[idx]
		for i := range lum {
			// How many frames REACHED this pixel, which is a different question from how many
			// contributed sky to it. Over a landscape most of the frame is ground and contributes
			// nothing, so counting contributions would report the sky band as the only covered
			// region — and cropping to that leaves a sliver.
			if reached[i] {
				covered[i]++
			}
			if !isSky(frame, lum, threshold, darkFloor, i) {
				continue
			}
			if v := float64(lum[i]); v < loL[i] || v > hiL[i] {
				// A σ-clipped outlier: a hot pixel, a satellite, an aircraft — or a meteor. Keep the
				// BRIGHTEST rejection per pixel rather than a mean: a meteor crosses in one frame, so
				// averaging it over the others is exactly how it disappears.
				if v > hiL[i] {
					tran.observe(i, idx, float32(v-hiL[i]), frame)
				}
				continue
			}
			for ch := 0; ch < c; ch++ {
				accum[ch][i] += float64(frame.Pix[ch][i])
			}
			counts[i]++
		}
	}); err != nil {
		return nil, nil, nil, err
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
	return out, covered, tran, nil
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

// horizonRefineBandFrac is how far either side of the pointing's horizon the real edge is looked
// for, as a fraction of the frame's extent along the split axis. Bounded on purpose: the pointing is
// good to about a degree, and a wider search only gives the gradient more chances to find something
// that is not the horizon.
const horizonRefineBandFrac = 0.06

// horizonMinDropFrac is how big the sky-to-ground step has to be, relative to that line's own sky
// level, before it is believed. The shoreline in the session this was written for sits about three
// times below the sky beside it, so this is a low bar that noise still cannot clear.
const horizonMinDropFrac = 0.06

// refineHorizon moves the pointing's straight horizon onto the edge the picture actually has.
//
// groundPrior can only ever produce a straight line: it takes the phone's altitude and cuts the frame
// at the matching coordinate. That is the right starting point and the wrong final answer — measured
// on this session, a strip of real dark shoreline ended up on the SKY side of that line, so the
// landscape layer never painted it and it rendered as a very dark band between the sea and the sky.
//
// The search is deliberately narrow-minded. It looks only within a bounded band around the prior, it
// takes the most NEGATIVE gradient rather than the largest — going down from sky into ground is a
// drop, while the town lights just below are a rise, and the lights are the stronger edge of the two —
// and it median-smooths the result across lines, because a horizon is continuous and a single line
// that locks onto a star must not drag the mask with it. Any line that finds nothing convincing keeps
// the prior, so a contrastless sea horizon degrades to exactly the old behaviour.
//
// It returns the refined ground mask and how far, on average, it moved.
func refineHorizon(prior []bool, lum []float32, w, h int) ([]bool, float64, bool) {
	if len(prior) != w*h || len(lum) != w*h || w < 8 || h < 8 {
		return nil, 0, false
	}
	// The prior is a half-plane, so its geometry is recoverable from the mask itself: whichever axis
	// it varies along is the axis it was cut on.
	alongX := false
	midRow := (h / 2) * w
	for x := 1; x < w; x++ {
		if prior[midRow+x] != prior[midRow+x-1] {
			alongX = true
			break
		}
	}
	alongY := false
	midCol := w / 2
	for y := 1; y < h; y++ {
		if prior[y*w+midCol] != prior[(y-1)*w+midCol] {
			alongY = true
			break
		}
	}
	if alongX == alongY {
		return nil, 0, false // neither varies, or both do: not the half-plane this understands
	}

	// nLines lines, each extent long, with at(line, v) reading the luminance.
	var nLines, extent int
	var at func(line, v int) float32
	var idx func(line, v int) int
	if alongX {
		nLines, extent = h, w
		at = func(line, v int) float32 { return lum[line*w+v] }
		idx = func(line, v int) int { return line*w + v }
	} else {
		nLines, extent = w, h
		at = func(line, v int) float32 { return lum[v*w+line] }
		idx = func(line, v int) int { return v*w + line }
	}

	// Where the prior cuts, and which side it calls sky.
	cut := -1
	for v := 1; v < extent; v++ {
		if prior[idx(nLines/2, v)] != prior[idx(nLines/2, v-1)] {
			cut = v
			break
		}
	}
	if cut < 0 {
		return nil, 0, false
	}
	skyLow := !prior[idx(nLines/2, 0)] // ground==true, so sky is where it is false
	step := 1
	if !skyLow {
		step = -1
	}

	band := int(horizonRefineBandFrac * float64(extent))
	if band < 4 {
		band = 4
	}
	// The gradient is measured across a few pixels rather than one: lum has already been blurred by
	// maskPreBlurFrac, so a single-pixel difference is mostly that blur.
	k := extent / 400
	if k < 2 {
		k = 2
	}

	found := make([]int, nLines)
	for line := 0; line < nLines; line++ {
		found[line] = -1
		lo, hi := cut-band, cut+band
		if lo < k+1 {
			lo = k + 1
		}
		if hi > extent-k-2 {
			hi = extent - k - 2
		}
		if lo >= hi {
			continue
		}
		// The line's own sky level, so the threshold does not depend on how bright the frame is.
		var skySum float64
		var skyN int
		for v := lo; v <= hi; v++ {
			if (skyLow && v < cut) || (!skyLow && v > cut) {
				skySum += float64(at(line, v))
				skyN++
			}
		}
		if skyN == 0 {
			continue
		}
		skyLevel := skySum / float64(skyN)
		if skyLevel <= 0 {
			continue
		}
		best, bestV := 0.0, -1
		for v := lo; v <= hi; v++ {
			d := float64(at(line, v+k*step) - at(line, v-k*step))
			if d < best {
				best, bestV = d, v
			}
		}
		if bestV >= 0 && -best >= horizonMinDropFrac*skyLevel {
			found[line] = bestV
		}
	}

	// Carry the prior wherever nothing was found, then smooth: a horizon is continuous.
	b := make([]int, nLines)
	any := 0
	for line := range b {
		if found[line] >= 0 {
			b[line] = found[line]
			any++
		} else {
			b[line] = cut
		}
	}
	if any < nLines/8 {
		return nil, 0, false // too little of the edge was actually seen to trust it
	}
	win := nLines / 20
	if win < 3 {
		win = 3
	}
	sm := medianSmoothInt(b, win)

	// The smoothed boundary is used directly rather than only where it disagrees with the raw one by
	// some tolerance. A median is already outlier-rejecting AND step-preserving: it drops the line
	// that locked onto a star while still following a real sloping shoreline. A tolerance on top of it
	// only adds a threshold to get wrong — set loosely it lets the star through, set tightly it
	// flattens a headland.
	var moved float64
	out := make([]bool, w*h)
	for line := 0; line < nLines; line++ {
		bv := sm[line]
		moved += math.Abs(float64(bv - cut))
		for v := 0; v < extent; v++ {
			if (skyLow && v > bv) || (!skyLow && v < bv) {
				out[idx(line, v)] = true
			}
		}
	}
	return out, moved / float64(nLines), true
}

// medianSmoothInt is a moving median over a line of boundary positions.
func medianSmoothInt(v []int, win int) []int {
	out := make([]int, len(v))
	buf := make([]int, 0, 2*win+1)
	for i := range v {
		buf = buf[:0]
		for j := i - win; j <= i+win; j++ {
			if j >= 0 && j < len(v) {
				buf = append(buf, v[j])
			}
		}
		sort.Ints(buf)
		out[i] = buf[len(buf)/2]
	}
	return out
}
