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
	// occludeGuardPx is how far past the occulter's fitted edge coverage is dropped, in the source
	// frame's pixels — the stack scales it by the drizzle factor to reach canonical ones.
	//
	// It only has to cover the EDGE'S OWN BLUR, not the occulter's motion across the window. Motion
	// is already handled exactly by masking each frame against its own fitted circle. What this
	// removes is the half-lit ring the point spread function leaves at the boundary, which is
	// neither Sun nor Moon and which would otherwise be averaged in as though it were disc.
	occludeGuardPx = 2.5
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
	// ScalePerFrame uses each frame's own fitted disc radius as its scale, instead of the robust
	// constant per source. Diagnostic only: the plate scale cannot change during a clip, so the
	// per-frame spread is measurement error and applying it smears the outer disc (regmodel.go).
	ScalePerFrame bool
	// RotationPerFrame uses each frame's own correlated rotation instead of the robust model fitted
	// across the clip. It exists to measure what the model is worth, and to fall back if a session
	// ever appears whose rotation genuinely is not smooth in time; see rotmodel.go for why the
	// per-frame estimates cannot be trusted individually.
	RotationPerFrame bool
	// NoDerotate skips the per-frame rotation estimate. A circle fit is rotation-blind, so rotation
	// has to be measured from disc structure — and when there is little structure to measure, the
	// estimate is noise that gets applied as if it were signal. At a 900 px radius a tenth of a
	// degree of error is 1.5 px of smear at the limb, so a bad estimate is far worse than none.
	NoDerotate bool
	// NoRefine keeps the fitted disc centre as the final answer for translation instead of refining it
	// by correlation (refine.go). Diagnostic: it is how the refinement's worth is measured.
	NoRefine bool
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
	// Moon is the occulting body on the CANONICAL raster, at the window's midpoint — R=0 when there
	// is none. The finish needs it to know which part of the master is a hole rather than a Sun, and
	// the midpoint is the honest single answer for a body that moved across the window.
	Moon   Limb
	Frames int
	Notes  []string
}

// Pair is the stack's geometry as the finish wants it, obscuration included.
//
// The fraction is filled in HERE rather than left to each caller because a master that does not
// carry its own phase cannot be placed in a sequence: ordering N windows across an eclipse needs
// the number, and recomputing it in three places is how the three answers start to disagree.
func (r *StackResult) Pair() Pair {
	p := Pair{Sun: r.Limb, Moon: r.Moon}
	p.Obscuration = OverlapFraction(p.Sun, p.Moon)
	return p
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
	res := &StackResult{}
	if !opts.ScalePerFrame {
		var notes []string
		usable, notes = StabiliseScale(usable)
		res.Notes = append(res.Notes, notes...)
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

	res.Limb = canonical
	weights := frameWeights(usable)
	ref, _, err := loadWarped(usable[refIndex(usable)], canonical, side, opts, nil, nil)
	if err != nil {
		return nil, err
	}
	refs := &stackRefs{full: ref, limb: canonical}
	if rf := usable[refIndex(usable)]; rf.Moon.R > 0 {
		rt := SolveTransform(rf.Limb, Limb{R: canonical.R / opts.drizzle()})
		refs.refMoon = circleToCanonical(rf.Moon, rt, side, opts.drizzle())
	}
	if !opts.NoRefine {
		refs.refiner = newRegRefiner(ref)
	}
	if opts.APAlign {
		// A reduced reference for field measurement only. Measuring at full resolution costs an order
		// of magnitude more time; how much reduction is affordable is a real trade-off, because the
		// residual this is trying to measure is itself sub-pixel at full scale.
		refs.small, refs.smallLimb = reduceCanonical(ref, canonical, opts.apScale())
	}

	// Both passes re-warp from disk rather than keeping the registered frames in memory. Holding
	// them would be the simpler code and is not an option at this size: three hundred frames on a
	// 2800-pixel canvas is nine gigabytes of float32. Everything registration measures is solved once
	// and cached across the passes instead.
	regs := make([]frameReg, len(usable))
	for i := range regs {
		regs[i].rot = math.NaN()
	}
	if !opts.NoDerotate && !opts.RotationPerFrame {
		res.Notes = append(res.Notes, solveRotations(ctx, usable, ref, canonical, regs)...)
	}
	// The occulter's sweep is pure geometry — it needs the fitted circles and the solved rotations,
	// and not one pixel — so it is resolved here, once, rather than per frame inside the passes.
	res.Moon = midWindowOcculter(usable, canonical, side, opts, regs)
	if refs.occluded = sweptOcculter(usable, canonical, side, opts, regs); refs.occluded != nil {
		n := 0
		for _, occ := range refs.occluded {
			if occ {
				n++
			}
		}
		res.Notes = append(res.Notes, fmt.Sprintf(
			"an occulting body covers %.1f%% of the canvas across this window and is excluded from the stack",
			100*float64(n)/float64(len(refs.occluded))))
	}
	master, covered, kept, notes, err := accumulateMaster(ctx, len(usable), side, opts.clipSigma(),
		func(i int) float64 { return weights[i] },
		func(i int) (*fits.Image, []bool, error) {
			return loadWarped(usable[i], canonical, side, opts, refs, &regs[i])
		})
	res.Notes = append(res.Notes, notes...)
	if err != nil {
		return nil, err
	}
	res.Frames = kept

	// The occulter's own limb, and the band its sweep took out of the Sun, come from a SECOND stack
	// registered on the Moon instead (pairstack.go). Without it that band is left to the finish to
	// invent.
	if res.Moon.R > 0 {
		mm, mcov, mnotes, merr := stackMoonAnchored(ctx, usable, canonical, res.Moon, side, opts, regs, weights)
		res.Notes = append(res.Notes, mnotes...)
		if merr != nil {
			res.Notes = append(res.Notes, "occulter-anchored stack: "+merr.Error()+
				" — the swept band is left empty")
		} else {
			compositeAnchors(master, covered, mm, mcov, side)
		}
	}

	res.Master = master
	return res, nil
}

// accumulateMaster is the two-pass sigma-clipped weighted mean, over whatever frames a loader hands
// it, and reports which output pixels ended up with any samples at all.
//
// It is a function rather than the body of Stack because the eclipse path stacks the SAME frames
// twice — once registered on the Sun, once on the occulter — and the two must combine arithmetic
// that is identical in every respect but the transform. Two copies of a two-pass clipped mean would
// be two chances for the weighting, the clipping or the empty-pixel rule to drift apart, and the
// composite of two masters that disagree about any of those shows up as a seam.
func accumulateMaster(ctx context.Context, n, side int, clip float64,
	weight func(int) float64, load func(int) (*fits.Image, []bool, error),
) (*fits.Image, []bool, int, []string, error) {

	var notes []string
	sum := make([]float64, side*side)
	sumSq := make([]float64, side*side)
	wsum := make([]float64, side*side)
	kept := 0
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, notes, err
		}
		im, cov, err := load(i)
		if err != nil {
			notes = append(notes, err.Error())
			continue
		}
		kept++
		w := weight(i)
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
		return nil, nil, 0, notes, fmt.Errorf("stack: every frame failed to register")
	}

	master := fits.NewImage(side, side, 1)
	covered := make([]bool, side*side)
	if kept < stackMinForClip {
		for k := range master.Pix[0] {
			if wsum[k] > 0 {
				master.Pix[0][k] = float32(sum[k] / wsum[k])
				covered[k] = true
			}
		}
		return master, covered, kept, notes, nil
	}

	// Second pass: the same frames again, now rejecting samples that disagree with the first pass.
	lim := make([]float64, side*side)
	mean := make([]float64, side*side)
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
	for i := 0; i < n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, 0, notes, err
		}
		im, cov, err := load(i)
		if err != nil {
			continue
		}
		w := weight(i)
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
			covered[k] = true
		case wsum[k] > 0:
			master.Pix[0][k] = float32(mean[k]) // everything clipped: keep the unclipped mean
			covered[k] = true
		default:
			uncovered++ // no frame reached here at all; it stays at zero
		}
	}
	if uncovered > 0 {
		notes = append(notes, fmt.Sprintf(
			"%.1f%% of the canvas was outside every frame and is left empty",
			100*float64(uncovered)/float64(len(master.Pix[0]))))
	}
	return master, covered, kept, notes, nil
}

// solveRotations fills rot with the rotation each frame needs, modelled rather than measured.
//
// It is a pass of its own, ahead of the two stacking passes, and that costs one extra read of every
// frame. The alternative — estimating each frame's rotation inside the first stacking pass, where the
// frame is already in memory, which is what this used to do — cannot work, because a robust model
// needs to see the whole clip's estimates before it can tell which of them are wrong. Reading twice
// to register correctly beats reading once to register a couple of degrees out: on a real two-clip
// session that error reached 39 px at the limb. Only the profiles are kept, 720 numbers a frame, so
// the pass costs I/O and nothing else.
func solveRotations(ctx context.Context, frames []Frame, ref *fits.Image, canonical Limb, regs []frameReg) []string {
	refProf := annulusProfile(ref, canonical)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))
	for i, f := range frames {
		if err := ctx.Err(); err != nil {
			return nil
		}
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			continue
		}
		raw[i], ok[i] = CorrelateRotation(refProf, annulusProfile(firstPlane(im), f.Limb))
	}
	modelled, notes := ModelRotations(frames, raw, ok)
	for i := range regs {
		regs[i].rot = modelled[i]
	}
	return notes
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

// stackRefs is the reference material every frame is registered against, prepared once from the
// reference frame and shared by both stacking passes.
type stackRefs struct {
	full      *fits.Image // the reference frame, already on the canonical raster
	limb      Limb        // the canonical geometry
	refiner   *regRefiner // reduced copies for the sub-pixel translation refinement; nil disables it
	small     *fits.Image // reduced copy for the distortion-field measurement; nil disables it
	smallLimb Limb
	// occluded is every canonical pixel the occulting body reaches in ANY frame of the window; nil
	// when there is no occulter. See sweptOcculter for why it is the window's union and not each
	// frame's own disc.
	occluded []bool
	// refMoon is the reference frame's occulting body on the canonical raster, R=0 when there is
	// none. The correlation refinement has to exclude it as well as each frame's own, or the target's
	// occulter simply slides under the reference's and votes anyway.
	refMoon Limb
}

// midWindowOcculter is the occulter's canonical geometry at the middle of the window.
//
// The median of the per-frame circles rather than any one frame's: the body moves steadily across a
// window, so the median is its position halfway through and is also the estimate least disturbed by
// a frame whose fit went wrong. It is what the finish paints, and what a later moon-anchored stack
// would register onto.
func midWindowOcculter(frames []Frame, canonical Limb, side int, opts StackOptions, regs []frameReg) Limb {
	var cx, cy, r []float64
	for i, f := range frames {
		if f.Moon.R <= 0 {
			continue
		}
		t := SolveTransform(f.Limb, Limb{R: canonical.R / opts.drizzle()})
		if i < len(regs) && !math.IsNaN(regs[i].rot) {
			t.RotDeg = regs[i].rot
		}
		m := circleToCanonical(f.Moon, t, side, opts.drizzle())
		cx, cy, r = append(cx, m.CX), append(cy, m.CY), append(r, m.R)
	}
	if len(r) == 0 {
		return Limb{}
	}
	return Limb{CX: median(cx), CY: median(cy), R: median(r)}
}

// sweptOcculter marks every canonical pixel the occulter covers at any point in the window.
//
// Masking each frame against ITS OWN occulter is the obvious choice and it is wrong, in a way worth
// writing down because the result looks better rather than worse. Each pixel would then average only
// the frames in which it happened to be Sun, so the band the limb sweeps across still fills in — at
// full brightness, from a dwindling number of frames, ending in a hard cut at the occulter's extreme
// position. That renders an edge SHARPER than the optics can produce (measured at 0.8 px against a
// single frame's 3.2), sitting a few pixels from where the Moon actually was, with a noise gradient
// running up to it. A synthetic edge in the right neighbourhood is a worse failure than a soft one,
// because nothing downstream can tell it is not real.
//
// So the whole swept band is excluded instead. Every surviving pixel is then backed by every frame
// in the window — uniform depth, uniform noise — and the occulter's own limb is left for a stack
// registered on the Moon rather than on the Sun, which is the only place it can come from honestly.
// The cost is the swept band itself, five pixels on a real thirty-second window.
func sweptOcculter(frames []Frame, canonical Limb, side int, opts StackOptions, regs []frameReg) []bool {
	var out []bool
	guard := occludeGuardPx * opts.drizzle()
	for i, f := range frames {
		if f.Moon.R <= 0 {
			continue
		}
		t := SolveTransform(f.Limb, Limb{R: canonical.R / opts.drizzle()})
		if i < len(regs) && !math.IsNaN(regs[i].rot) {
			t.RotDeg = regs[i].rot
		}
		if out == nil {
			out = make([]bool, side*side)
		}
		markOccluded(out, side, circleToCanonical(f.Moon, t, side, opts.drizzle()), guard)
	}
	return out
}

// frameReg is everything registration measures for one frame. It is measured on the first stacking
// pass and reused on the second — which halves the cost, and, more importantly, guarantees the two
// passes register the frame identically. A correlation re-run against the same inputs will agree, but
// nothing in the design would have required it to.
type frameReg struct {
	rot     float64 // rotation in degrees; NaN until solved
	dx, dy  float64 // residual translation after the limb fit, in canonical pixels
	refined bool
	field   *apField
}

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

// loadWarped reads a frame and maps it onto the canonical raster in ONE resample, however many terms
// the transform ends up carrying. reg, when supplied, caches everything measured here so the second
// stacking pass re-warps rather than re-registers.
func loadWarped(f Frame, canonical Limb, side int, opts StackOptions, refs *stackRefs, reg *frameReg) (*fits.Image, []bool, error) {
	im, err := fits.ReadImage(f.Path)
	if err != nil {
		return nil, nil, err
	}
	mono := firstPlane(im)
	t := SolveTransform(f.Limb, Limb{R: canonical.R / opts.drizzle()})
	switch {
	case opts.NoDerotate:
	case reg != nil && !math.IsNaN(reg.rot):
		t.RotDeg = reg.rot
	case refs != nil && refs.full != nil:
		deg := 0.0
		if d, ok := EstimateRotation(refs.full, mono, refs.limb, f.Limb); ok {
			deg = d
		}
		t.RotDeg = deg
		if reg != nil {
			reg.rot = deg
		}
	}
	// The fitted centre is where the limb fit THINKS the disc is; correlation says where it is. The
	// difference is folded back into the transform rather than corrected afterwards, so the frame is
	// still resampled exactly once — see refine.go.
	if refs != nil && refs.refiner != nil {
		if reg == nil || !reg.refined {
			occ := []Limb{refs.refMoon}
			if f.Moon.R > 0 {
				occ = append(occ, circleToCanonical(f.Moon, t, side, opts.drizzle()))
			}
			dx, dy := refs.refiner.measure(Warp(mono, t, side, opts.drizzle()), canonical, occ)
			if reg != nil {
				reg.dx, reg.dy, reg.refined = dx, dy, true
			} else {
				t = t.shiftCanonical(dx, dy, opts.drizzle())
			}
		}
		if reg != nil {
			t = t.shiftCanonical(reg.dx, reg.dy, opts.drizzle())
		}
	}
	// Every warp below goes through here so the occulter can never be forgotten on one of the three
	// paths. An occluded pixel is dropped from COVERAGE rather than dimmed or down-weighted: it is
	// not a bad sample of the Sun, it is not a sample of the Sun at all, and the stack already knows
	// exactly what to do with a pixel no frame reached.
	warp := func(field *apField) (*fits.Image, []bool) {
		im2, cov := warpCovered(mono, t, side, opts.drizzle(), field)
		if refs != nil && refs.occluded != nil {
			for k, occ := range refs.occluded {
				if occ {
					cov[k] = false
				}
			}
		}
		return im2, cov
	}

	if !opts.APAlign || refs == nil || refs.small == nil {
		im2, cov := warp(nil)
		return im2, cov, nil
	}
	// The field is measured once, on the rigidly-warped frame, and reused for the second stacking
	// pass. The measurement warp is discarded — only the final warp, which composes the similarity
	// and the field, ever touches the output.
	if reg != nil && reg.field != nil {
		im2, cov := warp(reg.field)
		return im2, cov, nil
	}
	// The measurement warp goes straight to the reduced raster, so the expensive full-resolution
	// resample happens exactly once per frame — for the output. It uses the REFINED transform, or the
	// field would re-measure the global residual that has just been removed and apply it twice.
	rigidSmall := Warp(mono, t, refs.small.W, opts.drizzle()*float64(refs.small.W)/float64(side))
	fld := measureAPFieldScaled(refs.small, rigidSmall, refs.smallLimb, opts.apGrid(side), side, 1)
	if reg != nil {
		reg.field = &fld
	}
	im2, cov := warp(&fld)
	return im2, cov, nil
}
