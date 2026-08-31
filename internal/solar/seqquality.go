package solar

import "github.com/verove-jordan/astronomy/internal/fits"

// MeasureSharpness reports how well a master resolves, preferring the occulter's edge when there is
// one.
//
// The choice matters more than it sounds. The solar limb is not a step — limb darkening on the way
// in, a chromospheric skirt and prominences on the way out — so its width mixes the optics with the
// Sun's own structure. The Moon is an opaque body against that Sun: a true knife edge, blurred by
// nothing but the system. Measured on the 12 Aug 2026 clips, blurring by sigma 1.0 against sigma 2.2
// separated by 2.1x on the occulter's edge and by only 1.35x with it masked out.
//
// That result holds for a SYNTHETIC blur and does not survive contact with a real clip, so this is
// the right probe for grading one image and the wrong one for ranking many. A panel master is graded
// here safely because the placement masks the occulter out, which leaves too few wedges and drops
// through to the solar limb on its own. Raw video frames still carry the Moon's interior, and there
// the measurement is dominated by what the codec did to the dark side of the crescent rather than by
// the seeing — see sharpestByItsOwnEdge, which is why frame selection does not use this.
//
// The plate scale is derived from the SOLAR radius even though the edge being read is the Moon's:
// the scale is a property of the image, and 2R is the Sun's angular diameter only for the Sun.
func MeasureSharpness(im *fits.Image, g Pair) PSF {
	if im == nil || g.Sun.R <= 0 {
		return PSF{}
	}
	if g.Moon.R > 0 {
		if psf := MeasureEdge(im, g.Moon, edgeRising, sunAngularDiameterArcsec/(2*g.Sun.R)); psf.OK {
			return psf
		}
	}
	return MeasurePSF(im, g.Sun)
}
