package solar

import (
	"context"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// stack.go combines registered frames into one master.
//
// It is a two-pass sigma-clipped weighted mean. The first pass measures each output pixel's mean
// and spread, the second averages again while rejecting samples that disagree with it — which is
// what removes an aircraft, a bird, a cosmic ray, or the one frame where the phone was nudged,
// without a median's noise penalty.
//
// Weights come from each frame's sharpness, but gently. Classic planetary lucky imaging weights
// hard and keeps a small fraction of frames, because seeing dominates there. At 40 mm the aperture
// is the limit and frame-to-frame variation is small, so the stack is SNR-limited: weighting hard
// would throw away signal to chase a sharpness difference that is mostly noise.

const (
	// stackWeightPow shapes the sharpness weighting. 1 is proportional; the planetary path uses 3.
	stackWeightPow = 1.0
	// stackWeightFloor keeps the softest frame contributing something rather than nothing.
	stackWeightFloor = 0.25
	// stackClipSigma is how far a sample may sit from the mean before it is rejected.
	stackClipSigma = 3.0
	// stackMinForClip is the frame count below which clipping is skipped — with too few samples the
	// measured spread is itself noise, and clipping on it removes signal.
	stackMinForClip = 6
)

// StackOptions tunes the stack.
type StackOptions struct {
	Drizzle    float64 // output scale: 1, 1.5 or 2
	CropMargin float64 // room past the limb, as a fraction of the radius
	ClipSigma  float64 // ≤0 → stackClipSigma
	// APAlign corrects atmospheric distortion with a grid of alignment points rather than one rigid
	// transform. Off, the stack is cleaner than a single frame but visibly softer everywhere.
	APAlign bool
	// APPoints overrides the grid density as a total point count; 0 sizes it from the disc.
	APPoints int
	// APScale is how much the raster is reduced before the field is MEASURED; 0 uses the default.
	// Measuring cheaply is only free while the residual being measured is larger than the reduced
	// grid can resolve — below that the reduction is itself a source of misalignment.
	APScale int
	// NoDerotate skips the per-frame rotation estimate. A circle fit is rotation-blind, so rotation
	// has to be measured from disc structure — and when there is little structure to measure, the
	// estimate is noise that gets applied as if it were signal. At a 900 px radius a tenth of a
	// degree of error is 1.5 px of smear at the limb, so a bad estimate is far worse than none.
	NoDerotate bool
}

// apGrid resolves the grid density for a canonical raster.
func (o StackOptions) apGrid(side int) int {
	if o.APPoints > 0 {
		n := int(math.Sqrt(float64(o.APPoints)) + 0.5)
		return clampInt(n, apGridMin, apGridMax)
	}
	return apGridN(side)
}

func (o StackOptions) drizzle() float64 {
	if o.Drizzle > 0 {
		return o.Drizzle
	}
	return 1
}

func (o StackOptions) margin() float64 {
	if o.CropMargin > 0 {
		return o.CropMargin
	}
	return defaultCropMargin
}

func (o StackOptions) apScale() int {
	if o.APScale > 0 {
		return o.APScale
	}
	return apMeasureScale
}

func (o StackOptions) clipSigma() float64 {
	if o.ClipSigma > 0 {
		return o.ClipSigma
	}
	return stackClipSigma
}

// StackResult is a stacked master and the geometry it was built on.
type StackResult struct {
	Master *fits.Image
	Limb   Limb
	Frames int
	Notes  []string
}

// Stack registers and combines frames into one master.
func Stack(ctx context.Context, frames []Frame, opts StackOptions) (*StackResult, error) {
	usable := make([]Frame, 0, len(frames))
	for _, f := range frames {
		if f.Limb.R > 0 {
			usable = append(usable, f)
		}
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("stack: no frame carries a fitted limb")
	}
	// The canonical geometry is the MEDIAN of the group, not the sharpest frame's. Anchoring on one
	// frame would bake that frame's own scale error into every other, and the median is the estimate
	// least disturbed by a bad fit.
	radii := make([]float64, len(usable))
	for i, f := range usable {
		radii[i] = f.Limb.R
	}
	canonical := Limb{R: median(radii)}
	side := CanonicalSide(canonical.R, opts.margin(), opts.drizzle())
	half := float64(side-1) / 2
	canonical.CX, canonical.CY = half, half
	canonical.R *= opts.drizzle()

	res := &StackResult{Limb: canonical}
	weights := frameWeights(usable)
	ref, _, err := loadWarped(usable[refIndex(usable)], canonical, side, opts, nil, Limb{}, nil, nil, nil, Limb{})
	if err != nil {
		return nil, err
	}
	// A reduced reference for field measurement only. Measuring at full resolution costs an order of
	// magnitude more time; how much reduction is affordable is a real trade-off, because the residual
	// this is trying to measure is itself sub-pixel at full scale.
	var refSmall *fits.Image
	var smallLimb Limb
	if opts.APAlign {
		refSmall, smallLimb = reduceCanonical(ref, canonical, opts.apScale())
	}

	// Both passes re-warp from disk rather than keeping the registered frames in memory. Holding
	// them would be the simpler code and is not an option at this size: three hundred frames on a
	// 2800-pixel canvas is nine gigabytes of float32. The rotation estimates are cached across the
	// passes instead, since measuring them is the expensive part of a warp.
	rot := make([]float64, len(usable))
	for i := range rot {
		rot[i] = math.NaN()
	}
	fields := make([]*apField, len(usable))
	sum := make([]float64, side*side)
	sumSq := make([]float64, side*side)
	wsum := make([]float64, side*side)
	kept := 0
	for i, f := range usable {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		im, cov, err := loadWarped(f, canonical, side, opts, ref, canonical, &rot[i], &fields[i], refSmall, smallLimb)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", f.Path, err))
			continue
		}
		kept++
		w := weights[i]
		for k, v := range im.Pix[0] {
			if !cov[k] {
				continue // outside this frame: contribute nothing rather than a fabricated edge pixel
			}
			d := float64(v)
			sum[k] += w * d
			sumSq[k] += w * d * d
			wsum[k] += w
		}
	}
	if kept == 0 {
		return nil, fmt.Errorf("stack: every frame failed to register")
	}
	res.Frames = kept

	master := fits.NewImage(side, side, 1)
	if kept < stackMinForClip {
		for k := range master.Pix[0] {
			if wsum[k] > 0 {
				master.Pix[0][k] = float32(sum[k] / wsum[k])
			}
		}
		res.Master = master
		return res, nil
	}

	// Second pass: the same frames again, now rejecting samples that disagree with the first pass.
	lim := make([]float64, side*side)
	mean := make([]float64, side*side)
	clip := opts.clipSigma()
	for k := range sum {
		if wsum[k] <= 0 {
			continue
		}
		mean[k] = sum[k] / wsum[k]
		if v := sumSq[k]/wsum[k] - mean[k]*mean[k]; v > 0 {
			lim[k] = clip * math.Sqrt(v)
		}
	}
	csum := make([]float64, side*side)
	cw := make([]float64, side*side)
	for i, f := range usable {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		im, cov, err := loadWarped(f, canonical, side, opts, ref, canonical, &rot[i], &fields[i], refSmall, smallLimb)
		if err != nil {
			continue
		}
		w := weights[i]
		for k, v := range im.Pix[0] {
			if !cov[k] {
				continue
			}
			d := float64(v)
			if lim[k] > 0 && math.Abs(d-mean[k]) > lim[k] {
				continue
			}
			csum[k] += w * d
			cw[k] += w
		}
	}
	var uncovered int
	for k := range master.Pix[0] {
		switch {
		case cw[k] > 0:
			master.Pix[0][k] = float32(csum[k] / cw[k])
		case wsum[k] > 0:
			master.Pix[0][k] = float32(mean[k]) // everything clipped: keep the unclipped mean
		default:
			uncovered++ // no frame reached here at all; it stays at zero
		}
	}
	if uncovered > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"%.1f%% of the canvas was outside every frame and is left empty", 100*float64(uncovered)/float64(len(master.Pix[0]))))
	}
	res.Master = master
	return res, nil
}

// refIndex picks the sharpest frame as the rotation reference.
func refIndex(frames []Frame) int {
	best := 0
	for i, f := range frames {
		if f.Score > frames[best].Score {
			best = i
		}
	}
	return best
}

// frameWeights turns sharpness scores into stack weights, normalised to a maximum of 1.
func frameWeights(frames []Frame) []float64 {
	best := 0.0
	for _, f := range frames {
		if f.Score > best {
			best = f.Score
		}
	}
	w := make([]float64, len(frames))
	for i, f := range frames {
		if best <= 0 {
			w[i] = 1
			continue
		}
		w[i] = math.Max(math.Pow(f.Score/best, stackWeightPow), stackWeightFloor)
	}
	return w
}

// loadWarped reads a frame and maps it onto the canonical raster. rotCache, when supplied, carries
// the frame's rotation between the two stacking passes so it is measured once rather than twice.
// reduceCanonical shrinks a canonical raster and its geometry by the given factor, for cheap field
// measurement.
func reduceCanonical(im *fits.Image, l Limb, factor int) (*fits.Image, Limb) {
	if factor < 1 {
		factor = 1
	}
	small, f := boxDownTo(im, im.W/factor)
	if f <= 0 {
		f = 1
	}
	return small, Limb{CX: l.CX / f, CY: l.CY / f, R: l.R / f}
}

func loadWarped(f Frame, canonical Limb, side int, opts StackOptions, ref *fits.Image, refLimb Limb,
	rotCache *float64, fieldCache **apField, refSmall *fits.Image, smallLimb Limb) (*fits.Image, []bool, error) {

	im, err := fits.ReadImage(f.Path)
	if err != nil {
		return nil, nil, err
	}
	mono := firstPlane(im)
	t := SolveTransform(f.Limb, Limb{R: canonical.R / opts.drizzle()})
	switch {
	case opts.NoDerotate:
	case rotCache != nil && !math.IsNaN(*rotCache):
		t.RotDeg = *rotCache
	case ref != nil:
		deg := 0.0
		if d, ok := EstimateRotation(ref, mono, refLimb, f.Limb); ok {
			deg = d
		}
		t.RotDeg = deg
		if rotCache != nil {
			*rotCache = deg
		}
	}
	if !opts.APAlign || ref == nil || refSmall == nil {
		im2, cov := warpCovered(mono, t, side, opts.drizzle(), nil)
		return im2, cov, nil
	}
	// The field is measured once, on the rigidly-warped frame, and reused for the second stacking
	// pass. The measurement warp is discarded — only the final warp, which composes the similarity
	// and the field, ever touches the output.
	if fieldCache != nil && *fieldCache != nil {
		im2, cov := warpCovered(mono, t, side, opts.drizzle(), *fieldCache)
		return im2, cov, nil
	}
	// The measurement warp goes straight to the reduced raster, so the expensive full-resolution
	// resample happens exactly once per frame — for the output.
	rigidSmall := Warp(mono, t, refSmall.W, opts.drizzle()*float64(refSmall.W)/float64(side))
	fld := measureAPFieldScaled(refSmall, rigidSmall, smallLimb, opts.apGrid(side), side, 1)
	if fieldCache != nil {
		*fieldCache = &fld
	}
	im2, cov := warpCovered(mono, t, side, opts.drizzle(), &fld)
	return im2, cov, nil
}
