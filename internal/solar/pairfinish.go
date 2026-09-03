package solar

import "math"

// pairfinish.go makes the solar finish work on a crescent, and it does so by giving that finish a
// whole Sun to look at rather than by teaching every step about the occulter.
//
// Six separate measurements inside the finish read "the disc": the instrument flat, the
// deconvolution, the limb-darkening profile, the tone curve's anchor, the off-limb halo model and
// the prominence reference. All six are radial or azimuthal averages, and the occulted region
// reaches the finish EMPTY — the stack excluded it — so all six would average zeros in. The symptoms
// are not subtle and none of them looks like its cause: a limb-darkening gain measured against
// annuli that are half zero comes out far too strong and over-brightens the crescent in rings; a
// tone curve anchored on percentiles that include the occulter renders the whole image too bright;
// the halo model, dragged down the same way, lets the prominence stretch render sky.
//
// Teaching each of them about the occulter would mean six masked variants and six chances for one to
// be missed later. So instead the occulted region is FILLED, before the finish, with the disc's own
// radial model measured from the part of the Sun that is visible — and painted back to background
// after it. The fill is legitimate rather than a trick: limb darkening is a function of radius and
// the Sun is radially symmetric, so the brightness at a given radius under the Moon really is the
// brightness measured at that radius elsewhere. Every average then sees the disc it was written for.
//
// It also removes the deconvolution's worst artefact for free. The occulted region arrives as a hard
// zero against a bright crescent — a step larger than the solar limb's — sitting INSIDE the disc,
// where extendDisc's protection does not reach. Richardson-Lucy rings on it, laying a bright rim and
// a dark band along the whole occulter. Filled, there is no step to ring on.

// occulterSkip returns the predicate every averaging step in the finish is given, or nil when there
// is no occulter and nothing to skip.
//
// A PREDICATE, NOT A FILLED DISC. Filling the hole with the disc's own radial model was tried first
// and shipped a bad picture, in two ways that both trace to the same mistake — inventing data and
// then measuring it.
//
// The tone curve anchors on the median inside 0.6 R. On a crescent that circle is almost entirely
// occulter, so the anchor became the FILL rather than any real Sun; the limb-darkening pass then
// boosted the actual crescent above that anchor, and everything past 1.3x of it clipped. The result
// was a burnt crescent on a crushed field, measured at 0.74% of pixels pinned at full red.
//
// And the occulter still had to be drawn afterwards, which meant painting a circle at the geometry's
// idea of where it was — over data whose own edge sat a few pixels elsewhere. That left a hard dark
// line through the crescent near the cusps and a stair-stepped boundary, both of which read
// instantly as artificial because they are.
//
// Skipping the occulter instead means every average sees only real Sun, and every pixel rendered is
// one the stack measured. There is nothing to line up because nothing is invented.
func occulterSkip(g Pair) func(x, y int) bool {
	if !g.Eclipsed() {
		return nil
	}
	guard := math.Max(pairMaskGuardPx, pairMaskGuardFrac*g.Sun.R)
	r2 := (g.Moon.R + guard) * (g.Moon.R + guard)
	return func(x, y int) bool {
		dx, dy := float64(x)-g.Moon.CX, float64(y)-g.Moon.CY
		return dx*dx+dy*dy <= r2
	}
}
