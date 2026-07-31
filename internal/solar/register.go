package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// register.go maps every frame onto one canonical geometry.
//
// The transform is a similarity — scale, rotation, translation — and it is derived rather than
// searched for. The fitted limb gives scale and translation directly and exactly: two frames of the
// Sun differ in scale by the ratio of their measured radii, whatever changed between them. That is
// what makes an afocal phone capture stackable at all, since re-seating the phone on the eyepiece
// changes the magnification and no amount of cross-correlation recovers an unknown scale reliably.
//
// Rotation is the one term the limb cannot supply — a circle is rotation-invariant — so it is
// measured separately, by correlating a mid-disc annulus against the reference. It matters more
// than it looks: on an alt-az mount the field rotates by a third of a degree per minute near the
// meridian, which at a 1200 px radius is seven pixels of motion at the limb over a single one-minute
// window. Left uncorrected the stack smears radially, and it gets misread as poor seeing.

const (
	// rotAnnulus is the radius, as a fraction of R, the rotation profile is sampled at. Far enough
	// in to be off the limb, far enough out to have a long arc and plenty of structure.
	rotAnnulus = 0.70
	// rotAnnulusHalfWidth is how thick that annulus is, as a fraction of R.
	rotAnnulusHalfWidth = 0.12
	// rotBins is the angular resolution of the profile: half a degree.
	rotBins = 720
	// rotMaxDeg bounds the search. Anything larger is a failed match, not a rotation.
	rotMaxDeg = 8.0
)

// Transform maps a frame onto the canonical raster.
type Transform struct {
	Scale    float64 // canonical radius / this frame's radius
	RotDeg   float64 // rotation to apply, degrees
	CX, CY   float64 // this frame's disc centre
	Measured bool
}

// SolveTransform derives the similarity taking a frame onto the canonical geometry.
func SolveTransform(frame, canonical Limb) Transform {
	if frame.R <= 0 || canonical.R <= 0 {
		return Transform{Scale: 1, CX: frame.CX, CY: frame.CY}
	}
	return Transform{Scale: canonical.R / frame.R, CX: frame.CX, CY: frame.CY, Measured: true}
}

// annulusProfile samples the mean brightness around a mid-disc annulus, as a function of angle.
func annulusProfile(im *fits.Image, l Limb) []float64 {
	if l.R <= 0 {
		return nil
	}
	prof := make([]float64, rotBins)
	counts := make([]int, rotBins)
	r0 := (rotAnnulus - rotAnnulusHalfWidth) * l.R
	r1 := (rotAnnulus + rotAnnulusHalfWidth) * l.R
	for b := 0; b < rotBins; b++ {
		a := 2 * math.Pi * float64(b) / rotBins
		cos, sin := math.Cos(a), math.Sin(a)
		for r := r0; r <= r1; r++ {
			x, y := l.CX+r*cos, l.CY+r*sin
			if x < 1 || y < 1 || x >= float64(im.W-2) || y >= float64(im.H-2) {
				continue
			}
			prof[b] += float64(imgops.SampleCubic(im.Pix[0], im.W, im.H, x, y))
			counts[b]++
		}
		if counts[b] > 0 {
			prof[b] /= float64(counts[b])
		}
	}
	// Remove the mean so the correlation responds to structure rather than to overall brightness,
	// which differs between frames even after photometric normalisation.
	var mean float64
	for _, v := range prof {
		mean += v
	}
	mean /= float64(len(prof))
	for i := range prof {
		prof[i] -= mean
	}
	return prof
}

// EstimateRotation returns the rotation, in degrees, taking target onto ref.
func EstimateRotation(ref, target *fits.Image, refLimb, targetLimb Limb) (float64, bool) {
	a, b := annulusProfile(ref, refLimb), annulusProfile(target, targetLimb)
	if a == nil || b == nil {
		return 0, false
	}
	maxLag := int(rotMaxDeg / 360 * rotBins)
	best, bestScore := 0, math.Inf(-1)
	scores := make(map[int]float64, 2*maxLag+1)
	for lag := -maxLag; lag <= maxLag; lag++ {
		var s float64
		for i := range a {
			s += a[i] * b[(i+lag+rotBins)%rotBins]
		}
		scores[lag] = s
		if s > bestScore {
			best, bestScore = lag, s
		}
	}
	if bestScore <= 0 || best <= -maxLag || best >= maxLag {
		return 0, false
	}
	// Parabolic vertex through the peak and its neighbours, for sub-bin precision.
	ym, y0, yp := scores[best-1], scores[best], scores[best+1]
	shift := 0.0
	if den := ym - 2*y0 + yp; math.Abs(den) > 1e-12 {
		shift = 0.5 * (ym - yp) / den
	}
	if math.Abs(shift) > 1 {
		shift = 0
	}
	return (float64(best) + shift) * 360 / rotBins, true
}

// Warp resamples a frame onto the canonical raster in ONE cubic pass.
//
// One pass is the point. Normalising scale first and warping afterwards would interpolate twice and
// cost roughly the MTF that drizzling onto a finer grid was meant to buy, so scale, rotation and
// translation are composed into a single coordinate map and the frame is sampled once.
func Warp(im *fits.Image, t Transform, side int, drizzle float64) *fits.Image {
	return warpWithField(im, t, side, drizzle, nil)
}

// warpWithField is Warp with an atmospheric distortion field applied on top, still in one pass.
//
// The field is defined in canonical (output) space, so it is added to the output coordinate BEFORE
// the inverse similarity maps it back to the source. That composition is what keeps this to a single
// interpolation: correcting distortion with a second resample would hand back in softening most of
// what the correction won.
func warpWithField(im *fits.Image, t Transform, side int, drizzle float64, field *apField) *fits.Image {
	out, _ := warpCovered(im, t, side, drizzle, field)
	return out
}

// warpCovered is warpWithField plus a coverage mask: false wherever the output pixel would have had
// to be sampled from outside the source frame.
//
// Tracking that matters whenever the disc runs past the frame edge, which a limb close-up does by
// definition and a slightly mis-framed full disc does by accident. The sampler clamps at the border,
// so an uncovered pixel comes back as a smeared copy of the frame's outermost row — fabricated data
// that looks plausible, stacks happily, and shows up in the result as a dark speckled arc along
// whichever edge cut the disc.
func warpCovered(im *fits.Image, t Transform, side int, drizzle float64, field *apField) (*fits.Image, []bool) {
	if drizzle <= 0 {
		drizzle = 1
	}
	out := fits.NewImage(side, side, 1)
	covered := make([]bool, side*side)
	half := float64(side-1) / 2
	rad := t.RotDeg * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	inv := 1 / (t.Scale * drizzle)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			ox, oy := float64(x), float64(y)
			if field != nil {
				// SUBTRACTED, not added. The field measures where the reference's feature is FOUND in
				// this frame, so undoing it means sampling back by that amount. Adding it instead
				// doubles the displacement — and the failure is quiet, because a doubly-misaligned
				// stack is smoother than a correct one, so it reads as "cleaner" on every simple
				// sharpness metric while actually being blurrier.
				fx, fy := field.at(ox, oy)
				ox, oy = ox-fx, oy-fy
			}
			dx, dy := (ox-half)*inv, (oy-half)*inv
			sx := t.CX + dx*cos - dy*sin
			sy := t.CY + dx*sin + dy*cos
			// One pixel of guard: the cubic kernel reaches two samples either side, so a coordinate
			// right on the border already pulls in clamped values.
			if sx < 1 || sy < 1 || sx > float64(im.W-2) || sy > float64(im.H-2) {
				continue
			}
			out.Pix[0][y*side+x] = imgops.SampleCubic(im.Pix[0], im.W, im.H, sx, sy)
			covered[y*side+x] = true
		}
	}
	return out, covered
}

// CanonicalSide is the raster size that holds a disc of the given radius plus its prominence margin.
func CanonicalSide(radius, margin, drizzle float64) int {
	if drizzle <= 0 {
		drizzle = 1
	}
	return (int(2*radius*(1+margin)*drizzle) + 1) &^ 1
}
