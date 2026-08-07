package solar

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/photom"
)

// norm.go brings a group's frames onto one photometric scale before they are stacked.
//
// It is what makes a deliberately bracketed session stackable at all — a real folder ranges over
// two orders of magnitude of exposure — and it is also what keeps an iPhone clip usable, since the
// ISP re-meters between frames and the disc brightness drifts through a capture.
//
// The correction is a monotone piecewise-linear LUT, not the scale-and-offset an affine fit would
// give. Against a camera pipeline that applies its own tone curve, an affine fit leaves a residual
// that is a function of intensity; on a limb-darkened disc intensity is a function of radius, so
// that residual becomes a radial error — a limb that breathes frame to frame, which the stack's
// sigma clipping then preferentially eats.

// Normalize rewrites every frame so its on-disc intensity distribution matches the group median.
// Frames whose curve cannot be measured are left untouched and reported.
func Normalize(frames []Frame) ([]string, error) {
	if len(frames) < 2 {
		return nil, nil
	}
	curves := make([]photom.FrameCurve, len(frames))
	measured := make([]bool, len(frames))
	var usable []photom.FrameCurve
	var warnings []string

	for i, f := range frames {
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			return warnings, fmt.Errorf("normalize: %w", err)
		}
		c, ok := photom.MeasureImageMasked(im, onDiscMask(f.Limb))
		if !ok {
			warnings = append(warnings, fmt.Sprintf("%s: no on-disc curve, left unnormalised", f.Path))
			continue
		}
		curves[i], measured[i] = c, true
		usable = append(usable, c)
	}
	if len(usable) < 2 {
		return warnings, nil
	}
	ref := photom.MedianCurve(usable)

	for i, f := range frames {
		if !measured[i] {
			continue
		}
		lut := buildLUT(curves[i].Q[:], ref.Q[:])
		if lut == nil {
			continue // already on the reference scale, or too degenerate to map
		}
		im, err := fits.ReadImage(f.Path)
		if err != nil {
			return warnings, fmt.Errorf("normalize: %w", err)
		}
		applyLUT(im.Pix[0], lut)
		if err := im.WriteFITS(f.Path); err != nil {
			return warnings, fmt.Errorf("normalize: %w", err)
		}
	}
	return warnings, nil
}

// onDiscMask restricts a measurement to the solar disc. Measuring the whole frame would let the
// sky — most of the pixels in a wide capture — dominate the percentiles and normalise the
// background instead of the subject.
func onDiscMask(l Limb) func(x, y int) bool {
	if l.R <= 0 {
		return func(int, int) bool { return true }
	}
	r2 := (medianRadius * l.R) * (medianRadius * l.R)
	return func(x, y int) bool {
		dx, dy := float64(x)-l.CX, float64(y)-l.CY
		return dx*dx+dy*dy <= r2
	}
}

// lutPoint is one knot of the intensity mapping.
type lutPoint struct{ from, to float64 }

// buildLUT pairs a frame's probe values with the reference's and returns a strictly increasing set
// of knots. Ties are dropped: a flat run in the source cannot be inverted, and forcing it would put
// a step in the mapping that shows up as banding on the smooth solar surface.
func buildLUT(src, ref []float64) []lutPoint {
	if len(src) != len(ref) || len(src) < 2 {
		return nil
	}
	pts := make([]lutPoint, 0, len(src))
	for i := range src {
		if i > 0 && src[i] <= pts[len(pts)-1].from {
			continue
		}
		if i > 0 && ref[i] <= pts[len(pts)-1].to {
			continue
		}
		pts = append(pts, lutPoint{from: src[i], to: ref[i]})
	}
	if len(pts) < 2 {
		return nil
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].from < pts[j].from })
	if identityLUT(pts) {
		return nil
	}
	return pts
}

// identityLUT reports whether the mapping would leave every probe essentially where it is, in which
// case rewriting the frame would only cost I/O and a rounding pass.
func identityLUT(pts []lutPoint) bool {
	for _, p := range pts {
		if math.Abs(p.to-p.from) > 1e-6*math.Max(1, math.Abs(p.from)) {
			return false
		}
	}
	return true
}

// applyLUT maps a plane through the knots in place, interpolating between them and extrapolating
// beyond the ends — upward with the slope of the last segment, so a highlight brighter than any
// probe stays brighter rather than being flattened onto the final knot, and downward through the
// ORIGIN.
//
// The two ends are not symmetric, and getting the lower one wrong is expensive. The curve is
// measured ON THE DISC, because measuring it over the whole frame would let the sky — most of the
// pixels in a solar raster — define the percentiles and normalise the background instead of the
// subject. That leaves everything outside the limb below the lowest knot, extrapolated rather than
// fitted: the sky, and with it every prominence. Continuing the lowest segment's slope down there is
// an affine extrapolation over a range several times wider than the one it was fitted on, and on
// real frames it drove the sky to MINUS eight percent of the disc — a floor no stretch can recover
// from, sitting exactly where the faint things live. Proportional extrapolation is the only model
// with a defensible boundary condition: no light must map to no light.
func applyLUT(p []float32, pts []lutPoint) {
	loScale := 1.0
	if pts[0].from > 0 {
		loScale = pts[0].to / pts[0].from
	}
	hiSlope := segSlope(pts[len(pts)-2], pts[len(pts)-1])
	for i, v := range p {
		x := float64(v)
		switch {
		case x <= pts[0].from:
			p[i] = float32(x * loScale)
		case x >= pts[len(pts)-1].from:
			last := pts[len(pts)-1]
			p[i] = float32(last.to + (x-last.from)*hiSlope)
		default:
			j := sort.Search(len(pts), func(k int) bool { return pts[k].from > x }) - 1
			a, b := pts[j], pts[j+1]
			t := (x - a.from) / (b.from - a.from)
			p[i] = float32(a.to + t*(b.to-a.to))
		}
	}
}

// segSlope is the slope of one LUT segment, guarding a degenerate span.
func segSlope(a, b lutPoint) float64 {
	if d := b.from - a.from; math.Abs(d) > 1e-12 {
		return (b.to - a.to) / d
	}
	return 1
}
