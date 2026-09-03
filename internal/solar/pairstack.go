package solar

import (
	"context"
	stderrors "errors"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// pairstack.go stacks the same frames a second time, registered on the OCCULTER instead of the Sun.
//
// The sun-anchored stack leaves a hole. It has to: a pixel the lunar limb sweeps across during a
// window is Moon in some frames and Sun in others, and the only honest way to combine those is not
// to — so the occulter's whole sweep is dropped from coverage, and what comes out is a band of empty
// canvas about six pixels wide wrapped around the occulter. On a crescent twenty pixels across near
// maximum, that is a third of the visible Sun missing, at exactly the edge the eye goes to. Left
// alone the finish invents it, smoothly and plausibly, from the disc's radial model.
//
// Registered on the Moon instead, that band is real. The occulter is stationary by construction, so
// every frame contributes to every pixel of it — uniform depth, uniform noise — and the Sun, which
// now moves, is smeared by at most half the window's drift. That trade is worth taking exactly where
// the sun anchor has nothing: two and a half pixels of smear against six pixels of invention.
//
// WHAT THIS DOES NOT BUY, at these plate scales, is the occulter's own limb detail. The Moon's limb
// profile carries mountains of three or four kilometres, which at 384,000 km is about 1.6 arcseconds
// — half a pixel at 3.14"/px. The edge is a smooth circle here whatever we do, and a synthetic one
// of the right width would look the same. The band of real Sun beside it is the whole point.

const (
	// moonBandPx is how far past the occulter's edge the occulter-anchored master is accumulated,
	// in canonical pixels before drizzle.
	//
	// It only has to cover the sweep the sun anchor dropped — the drift plus its guard — with enough
	// margin to blend across. Reaching further would not be wrong so much as pointless: every pixel
	// further out is Sun that the sun-anchored stack already has sharper.
	moonBandPx = 14.0
	// moonBlendPx is the width of the crossfade between the two anchors, in canonical pixels. About a
	// point spread function wide: enough that the join is not a step, narrow enough that the smeared
	// master does not reach into Sun the sharp one has.
	moonBlendPx = 2.5
	// moonMinDriftPx is the sweep below which the second stack is not worth running. Under a pixel of
	// drift the sun anchor's hole is the guard alone, which the finish's own fill covers without
	// visible error, and a whole second pass over every frame buys nothing.
	moonMinDriftPx = 1.0
)

// stackMoonAnchored builds a master registered on the occulting body, over a band around it.
func stackMoonAnchored(ctx context.Context, frames []Frame, canonical, mid Limb, side int,
	opts StackOptions, regs []frameReg, weights []float64) (*fits.Image, []bool, []string, error) {

	if drift := occulterDrift(frames, canonical, side, opts, regs); drift < moonMinDriftPx*opts.drizzle() {
		return nil, nil, []string{}, errNoSweep
	}
	band := mid.R + moonBandPx*opts.drizzle()
	master, covered, kept, notes, err := accumulateMaster(ctx, len(frames), side, opts.clipSigma(),
		func(i int) float64 { return weights[i] },
		func(i int) (*fits.Image, []bool, error) {
			return loadMoonWarped(frames[i], canonical, mid, side, opts, &regs[i], band)
		})
	if err != nil {
		return nil, nil, notes, err
	}
	notes = append(notes, occulterAnchorNote(kept, mid, band))
	return master, covered, notes, nil
}

// errNoSweep reports that the occulter barely moved, so a second stack has nothing to recover.
var errNoSweep = stderrors.New(
	"the occulter barely moved across this window, so the Sun-anchored stack lost nothing to recover")

// occulterDrift is how far the occulter travelled across the window, in canonical pixels.
func occulterDrift(frames []Frame, canonical Limb, side int, opts StackOptions, regs []frameReg) float64 {
	var first, last Limb
	have := false
	for i, f := range frames {
		if f.Moon.R <= 0 {
			continue
		}
		m := circleToCanonical(f.Moon, moonFrameTransform(f, canonical, opts, regs, i), side, opts.drizzle())
		if !have {
			first, have = m, true
		}
		last = m
	}
	if !have {
		return 0
	}
	return math.Hypot(last.CX-first.CX, last.CY-first.CY)
}

// moonFrameTransform is the sun-anchored similarity for one frame, with its solved rotation applied.
func moonFrameTransform(f Frame, canonical Limb, opts StackOptions, regs []frameReg, i int) Transform {
	t := SolveTransform(f.Limb, Limb{R: canonical.R / opts.drizzle()})
	if !opts.NoDerotate && i < len(regs) && !math.IsNaN(regs[i].rot) {
		t.RotDeg = regs[i].rot
	}
	return t
}

// loadMoonWarped maps a frame onto the canonical raster with the OCCULTER, not the Sun, held still.
//
// The two anchors differ by a translation and nothing else. Scale and rotation belong to the optics
// and the mount, so they are taken unchanged from the sun-anchored solution; only the frame's own
// occulter is then slid onto the window's mid-point position. Solving the scale from the occulter
// instead would be measuring the same plate scale from a shorter arc, which is the same answer with
// more noise in it.
//
// The distortion field is deliberately not applied. It was measured against the sun-anchored
// reference and describes where the SUN's features sit; carrying it into a frame registered on the
// Moon would apply a correction to content it was never measured on. Over a band a few pixels wide
// the field's amplitude is sub-pixel anyway.
func loadMoonWarped(f Frame, canonical, mid Limb, side int, opts StackOptions,
	reg *frameReg, band float64) (*fits.Image, []bool, error) {

	if f.Moon.R <= 0 {
		return nil, nil, errNoOcculterInFrame
	}
	im, err := fits.ReadImage(f.Path)
	if err != nil {
		return nil, nil, err
	}
	mono := firstPlane(im)
	var regs []frameReg
	if reg != nil {
		regs = []frameReg{*reg}
	}
	t := moonFrameTransform(f, canonical, opts, regs, 0)

	// Where this frame's occulter would land under the sun anchor, and the canonical-space nudge that
	// puts it on the window's mid-point instead. The sign follows shiftCanonical's contract: a shift
	// of (dx,dy) moves content by (dx,dy) in the output.
	m := circleToCanonical(f.Moon, t, side, opts.drizzle())
	t = t.shiftCanonical(mid.CX-m.CX, mid.CY-m.CY, opts.drizzle())

	im2, cov := warpCovered(mono, t, side, opts.drizzle(), nil)
	keepBand(cov, side, mid, band)
	return im2, cov, nil
}

// errNoOcculterInFrame is the per-frame skip when a frame's occulter was never fitted.
var errNoOcculterInFrame = stderrors.New("no occulter fitted in this frame")

// keepBand drops coverage everywhere outside a radius of the occulter's centre, so the second stack
// only ever contributes where the first one has nothing.
func keepBand(cov []bool, side int, mid Limb, band float64) {
	b2 := band * band
	for y := 0; y < side; y++ {
		dy := float64(y) - mid.CY
		for x := 0; x < side; x++ {
			dx := float64(x) - mid.CX
			if dx*dx+dy*dy > b2 {
				cov[y*side+x] = false
			}
		}
	}
}

// compositeAnchors fills the sun-anchored master's hole from the occulter-anchored one, in place.
//
// The join is made by BLURRING THE SUN'S COVERAGE MASK rather than by a distance from any circle,
// and that is what makes it correct at the shape the hole actually has. The swept region is not a
// disc — it is the occulter's disc dragged along the direction it travelled, a stadium — so a radial
// crossfade about the occulter's centre would be too wide across the motion and too narrow along it,
// leaving a visible lens of one master's noise on two sides. A blurred mask feathers whatever the
// boundary turns out to be, at a width set once.
func compositeAnchors(sun *fits.Image, sunCov []bool, moon *fits.Image, moonCov []bool, side int) {
	if moon == nil || len(moonCov) != len(sunCov) {
		return
	}
	w := make([]float32, len(sunCov))
	for i, c := range sunCov {
		if c {
			w[i] = 1
		}
	}
	w = imgops.GaussianBlur(w, side, side, moonBlendPx)
	for i := range sun.Pix[0] {
		if !moonCov[i] {
			continue // nothing to offer here; whatever the sun anchor has, including nothing, stands
		}
		t := float64(w[i])
		if !sunCov[i] {
			// The blurred mask leaks a little weight into pixels the sun anchor never covered. Those
			// have no sun value to weigh — they are zero — so honouring the weight there would darken
			// the very band this exists to fill.
			t = 0
		}
		sun.Pix[0][i] = float32(t*float64(sun.Pix[0][i]) + (1-t)*float64(moon.Pix[0][i]))
	}
	// Coverage grows to the union: the finish decides what is a hole from this.
	for i, c := range moonCov {
		if c {
			sunCov[i] = true
		}
	}
}

// occulterAnchorNote describes what the second stack recovered.
func occulterAnchorNote(kept int, mid Limb, band float64) string {
	return fmt.Sprintf("the band the occulter's sweep took out of the Sun was recovered from a second "+
		"stack registered on the occulter (%d frames, out to %.0f px past its edge)", kept, band-mid.R)
}
