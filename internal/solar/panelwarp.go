package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// WarpPanel resamples one finished phase onto the sequence's shared raster: Sun centred, Sun at the
// requested radius, North up, East left, and — when the Sun was low enough for it to show —
// refraction's flattening taken back out.
//
// It is a general 2x2 map rather than the similarity Warp uses, because two of the four corrections
// are not similarities. Mirroring is a reflection, and undoing refraction is a stretch along ONE
// axis: near the horizon the disc is compressed vertically and not horizontally, so putting it back
// requires a transform that treats the two directions differently. Composing all four into a single
// matrix keeps this to one cubic resample, the same discipline Warp follows.
func WarpPanel(im *fits.Image, f PanelFrame, o Orientation, radius float64, side int) *fits.Image {
	out := fits.NewImage(side, side, im.C)
	if im == nil || f.Sun.R <= 0 || radius <= 0 || side <= 0 {
		return out
	}
	inv := invert2(panelMatrix(f, o, radius))
	half := float64(side-1) / 2
	// The cut has to clear the disc AFTER the refraction stretch, which lengthens one axis by
	// 1/flatten. Sized on the round disc it would slice the ends off the very panels that needed
	// straightening.
	keep, fade := discKeepR, discFadeR
	if f.Flatten > 0 && f.Flatten < 1 {
		keep, fade = keep/f.Flatten, fade/f.Flatten
	}
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			dx, dy := float64(x)-half, float64(y)-half
			r := math.Hypot(dx, dy)
			w := panelVignette(r/half) * discCut(r/radius, keep, fade)
			if w <= 0 {
				continue
			}
			sx := f.Sun.CX + inv[0]*dx + inv[1]*dy
			sy := f.Sun.CY + inv[2]*dx + inv[3]*dy
			// One pixel of guard: the cubic kernel reaches two samples either side, so a coordinate
			// on the border already pulls in clamped values.
			if sx < 1 || sy < 1 || sx > float64(im.W-2) || sy > float64(im.H-2) {
				continue
			}
			for c := 0; c < im.C; c++ {
				out.Pix[c][y*side+x] = float32(w) * imgops.SampleCubic(im.Pix[c], im.W, im.H, sx, sy)
			}
		}
	}
	return out
}

// Panel vignette bounds, as a fraction of the panel's own half-width — NOT of the solar radius.
//
// A panel is a disc on black sky, not a rectangle of it. Whatever the raster holds, it ends
// somewhere, and compositing a square whose content stops abruptly leaves that boundary visible: as
// a lit box where the finish's sky pedestal reaches the corner, and as an octagon inside it where
// the source frame did not. Keying the fade to the raster rather than to the disc means it always
// completes exactly at the edge, whatever margin the master was cut with.
const (
	// panelKeepFrac is where the fade starts, as a fraction of the half-width.
	panelKeepFrac = 0.90
	// panelFadeFrac is where it reaches zero — the raster's own edge.
	panelFadeFrac = 1.0
)

// Off-limb cut, in solar radii. The instrument puts a ring just outside the limb — a diffraction and
// internal-reflection artefact that sits in the source frames themselves, dark on some phases and
// bright on others. It is not the Sun, it moves with the optics rather than with the sky, and on a
// sheet of seven panels it is drawn seven times. Beyond a few percent of the radius there is
// nothing on a sheet panel worth keeping, so everything past it goes.
//
// The dedicated chromosphere picture does NOT go through here: prominences are exactly what it is
// for, and they live in the band this throws away.
const (
	discKeepR = 1.025
	discFadeR = 1.070
)

// discCut is the weight at a radius in solar radii: 1 over the disc and its immediate surround, then
// a smoothstep to nothing.
func discCut(r, keep, fade float64) float64 {
	if r <= keep {
		return 1
	}
	if r >= fade {
		return 0
	}
	t := (fade - r) / (fade - keep)
	return t * t * (3 - 2*t)
}

// panelVignette is the weight at a radius given as a fraction of the panel's half-width: 1 out to
// panelKeepFrac, then a smoothstep to 0. Smooth rather than a hard cut because the sheet composites
// by max, and a hard edge inside a neighbouring panel's glow would draw a visible arc across it.
func panelVignette(r float64) float64 {
	if r <= panelKeepFrac {
		return 1
	}
	if r >= panelFadeFrac {
		return 0
	}
	t := (panelFadeFrac - r) / (panelFadeFrac - panelKeepFrac)
	return t * t * (3 - 2*t)
}

// panelMatrix composes, in order applied to a source offset from the Sun's centre:
// scale to the shared radius, mirror if the train flips, roll into the sky frame, then stretch the
// vertical back out. Returned row-major as {a, b, c, d} for [[a b],[c d]].
func panelMatrix(f PanelFrame, o Orientation, radius float64) [4]float64 {
	m := scaleMatrix(radius / f.Sun.R)
	if o.Mirrored {
		m = mul2([4]float64{-1, 0, 0, 1}, m)
	}
	rad := o.RollDeg * math.Pi / 180
	m = mul2([4]float64{math.Cos(rad), -math.Sin(rad), math.Sin(rad), math.Cos(rad)}, m)
	return mul2(deflattenMatrix(f), m)
}

// deflattenMatrix stretches by 1/flatten along the local vertical, which sits at the parallactic
// angle in the output's own frame. Written as I + (1/f - 1)·u·uᵀ: the identity everywhere except
// along u, which is exactly what a one-axis stretch is.
func deflattenMatrix(f PanelFrame) [4]float64 {
	if f.Flatten <= 0 || f.Flatten >= 1 {
		return [4]float64{1, 0, 0, 1}
	}
	a := skyAngle(f.ParallacticDeg)
	ux, uy := math.Cos(a), math.Sin(a)
	k := 1/f.Flatten - 1
	return [4]float64{1 + k*ux*ux, k * ux * uy, k * ux * uy, 1 + k*uy*uy}
}

func scaleMatrix(s float64) [4]float64 { return [4]float64{s, 0, 0, s} }

func mul2(a, b [4]float64) [4]float64 {
	return [4]float64{
		a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3],
	}
}

// invert2 inverts a 2x2. A singular matrix cannot arise from the compositions above — every factor
// has a non-zero determinant — but a degenerate scale would, so it falls back to the identity rather
// than filling the panel with infinities.
func invert2(m [4]float64) [4]float64 {
	det := m[0]*m[3] - m[1]*m[2]
	if det == 0 || math.IsNaN(det) {
		return [4]float64{1, 0, 0, 1}
	}
	return [4]float64{m[3] / det, -m[1] / det, -m[2] / det, m[0] / det}
}
