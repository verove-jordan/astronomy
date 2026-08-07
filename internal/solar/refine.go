package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// refine.go closes the gap between where the limb fit says a frame is and where it actually is.
//
// Registration here derives the similarity rather than searching for it: the fitted circle gives
// scale and translation directly, which is what makes an afocal phone capture stackable at all, since
// re-seating the phone changes the magnification and no correlation recovers an unknown scale
// reliably. The mistake was treating that as the FINAL answer for translation as well.
//
// It is not, and the frames say so. Measured on a real 1.06"/px clip, the fitted centre deviates from
// its own smooth trend by 3.83 px in x against 0.50 px in y — a 7.7x axis asymmetry that cannot be
// physical, since the Sun does not know which way the sensor is oriented. The stack agrees: its
// left and right limb, where the edge normal lies along x, measure a point spread function of 4.1 px
// against 3.4 px top and bottom. That extra x-only smear is what a per-frame centring error produces,
// and it is why a stack of frames each resolving 1.6 px came out at 3.4.
//
// So the circle fit initialises, and a correlation over the whole disc finishes the job. Two things
// keep that honest:
//
//   - It is folded into the transform, not applied as a second warp. Correcting a two-pixel error by
//     interpolating twice hands back in softening most of what the correction won.
//   - It runs coarse-to-fine over box-reduced copies, because a full-scale search spends nearly all
//     of its work discovering roughly where the match is — a question a reduced raster answers
//     perfectly well — and only the last pixel genuinely needs resolution.
//
// Reference and frame are reduced by the SAME box average, deliberately. Decimating one with a box
// filter and the other with a cubic sampler leaves them aliased differently, and the correlation peak
// then carries a sub-pixel bias that varies with the shift being measured — the exact quantity being
// measured here.

const (
	// regRefineWindow is the correlation window half-size, as a fraction of the canonical radius.
	//
	// It reaches the limb rather than stopping short of it. The limb is by far the highest-contrast
	// feature present and it is precisely the one the circle fit misplaced, so excluding it would
	// leave the correlation to be decided by low-contrast disc texture — and on quiet Sun there can be
	// very little of that.
	regRefineWindow = 1.0
	// regRefineBlur is the pre-blur applied at each rung, in that rung's own pixels, so the match
	// follows structure rather than noise. It is applied ONCE per rung rather than inside the
	// correlator, which blurs whatever it is handed every time it is called.
	regRefineBlur = 1
)

// regPass is one rung of the ladder: how far the raster is reduced, and how far it then searches in
// that rung's own pixels around whatever the previous rung found.
type regPass struct {
	reduce int
	search float64
}

// regLadder is the schedule. Each rung's search window has to cover the previous rung's own
// resolution: a rung locates the peak to about half of its pixels, so reducing by a factor of two
// between rungs and searching two of the finer rung's pixels covers it with room to spare.
//
// The range at the top is what bounds the whole thing: eight-fold reduction searching two of its
// pixels reaches ±16 canonical pixels, comfortably past the 3.8 px spread the fit actually shows.
var regLadder = []regPass{{reduce: 8, search: 2}, {reduce: 4, search: 2}, {reduce: 2, search: 2}}

// regRefiner holds the reference, reduced and blurred once per rung, ready to correlate every frame
// against.
type regRefiner struct {
	ref []*fits.Image
}

// newRegRefiner prepares the reference rungs. It returns nil when the reference is too small to
// reduce meaningfully, in which case registration simply keeps the fitted centre.
func newRegRefiner(ref *fits.Image) *regRefiner {
	if ref == nil || ref.W < 8*regLadder[0].reduce {
		return nil
	}
	r := &regRefiner{}
	for _, p := range regLadder {
		// Whatever reduction boxDownTo lands on is the one used, not the one asked for. It divides so
		// the long edge is AT MOST the requested size, which rounds the factor up whenever the raster
		// is not a multiple of it — a 2130 px canvas asked to reduce by eight reduces by nine. Insisting
		// on the exact factor here made this whole stage return nil on every real raster while every
		// synthetic fixture, sized in round numbers, went on passing.
		//
		// The factor only has to be the SAME for the reference and the frame, and it is: both call this
		// with the same requested edge on rasters of the same size.
		small, f := boxDownTo(ref, ref.W/p.reduce)
		if f < 2 {
			return nil // nothing was reduced; the ladder would be three copies of one full-scale search
		}
		r.ref = append(r.ref, blurPlane(small, regRefineBlur))
	}
	return r
}

// measure returns the translation, in canonical pixels, still separating a rigidly warped frame from
// the reference — in the same sense the distortion field uses, so it can be folded in the same way.
func (r *regRefiner) measure(rigid *fits.Image, l Limb) (dx, dy float64) {
	if r == nil || rigid == nil {
		return 0, 0
	}
	for i, p := range regLadder {
		ref := r.ref[i]
		small, f := boxDownTo(rigid, rigid.W/p.reduce)
		if f <= 0 || small.W != ref.W || small.H != ref.H {
			continue
		}
		tgt := blurPlane(small, regRefineBlur)
		radius := int(regRefineWindow * l.R / f)
		if radius < 8 {
			continue
		}
		sx, sy := comet.AlignSeeded(ref, tgt, comet.Point{X: l.CX / f, Y: l.CY / f}, radius,
			p.search, 0, dx/f, dy/f)
		dx, dy = sx*f, sy*f
	}
	return dx, dy
}

// shiftCanonical folds a translation measured in canonical pixels back into the frame's own disc
// centre, so the correction costs nothing at all: the warp already reads that centre, and the frame
// is still resampled exactly once.
//
// The sign follows the distortion field's convention — a measured displacement is SUBTRACTED from the
// output coordinate — because both describe the same thing, where the reference's content is found in
// this frame. Getting it backwards doubles the error rather than removing it, and does so quietly: a
// doubly-misaligned stack is smoother than a correct one, so it reads as an improvement on every
// simple sharpness metric.
func (t Transform) shiftCanonical(dx, dy, drizzle float64) Transform {
	if drizzle <= 0 {
		drizzle = 1
	}
	if t.Scale <= 0 {
		return t
	}
	inv := 1 / (t.Scale * drizzle)
	rad := t.RotDeg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	t.CX -= inv * (dx*cos - dy*sin)
	t.CY -= inv * (dx*sin + dy*cos)
	return t
}
