// Package guidestar measures where one star sits on the sensor, to a fraction of a pixel, frame
// after frame.
//
// It exists to feed periodic-error training: a worm error of a few arcseconds, sampled every second
// or two for the best part of an hour, on a rig where one pixel is about an arcsecond. That sets the
// bar — tenths of a pixel, with no systematic bias — and it is the bias that is hard, not the noise.
//
// # Why not reuse the focus meter's centroid
//
// internal/focus already computes a flux-weighted centroid inside hfdAt, and it would have been the
// obvious thing to export. It is the wrong estimator here. It sums pixels above a hard threshold
// (background plus three sigma), and a hard threshold makes the estimator non-linear: as seeing moves
// the star a fraction of a pixel, edge pixels flicker in and out of the sum, and the answer is pulled
// toward whole-pixel positions. That "pixel locking" is a fraction of a pixel — invisible in a focus
// reading, which only wants a trend, and the same size as the entire signal being measured here.
//
// So this uses a windowed centroid instead: weight every pixel by a Gaussian centred on the current
// best estimate, and iterate. The weight varies smoothly with position, so there is no threshold to
// snap to, and the answer stays linear in the true offset. It is what SExtractor calls a windowed
// position and what guiding software has used for years.
package guidestar

import (
	"errors"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// ErrNoStar is returned when nothing usable was found. It is an ordinary outcome — cloud, a passing
// aeroplane, a dewed-up corrector — not a failure, and callers mark the sample invalid and carry on
// rather than moving the mount to hunt.
var ErrNoStar = errors.New("no usable guide star")

// windowPx is the half-size of the box a star is measured in. Big enough for a badly seeing-bloated
// star, small enough that a neighbour rarely intrudes.
const windowPx = 12

// Star is one measured star.
type Star struct {
	X, Y float64 // sub-pixel position, in the coordinates of the image passed in
	// Peak is the background-subtracted height, and Flux the total above background. Both are
	// tracked over a run: a star that fades has dewed or clouded over, and its positions should be
	// distrusted before they are silently folded into a curve.
	Peak float64
	Flux float64
	HFD  float64
	// SNR is peak over the local background noise.
	SNR float64
}

// Options tune acquisition.
type Options struct {
	// SaturationFraction rejects stars whose peak is above this fraction of full scale. A clipped
	// star has a flat top, and a flat top has no centroid worth the name — it just sits wherever the
	// clipped plateau happens to be.
	SaturationFraction float32
	// MinSNR is the floor below which a detection is not trusted.
	MinSNR float64
}

func (o Options) withDefaults() Options {
	if o.SaturationFraction <= 0 {
		o.SaturationFraction = 0.7
	}
	if o.MinSNR <= 0 {
		o.MinSNR = 10
	}
	return o
}

// Pick chooses the best star in the frame to follow.
//
// "Best" is the brightest one that is not saturated, not a hot pixel, not too close to an edge and
// not crowded by a neighbour — in that order, because brightness buys precision and everything else
// on the list is a way of getting a confident answer that is wrong.
func Pick(im *fits.Image, o Options) (Star, error) {
	o = o.withDefaults()
	peaks := postprocess.DetectStarPeaks(im, postprocess.StarDetectOptions{
		Sigma:    6,
		MaxStars: 200,
		// Two windows apart, so no candidate's measuring box overlaps another's core.
		MinSepPx:   2 * windowPx,
		SatLevel:   o.SaturationFraction,
		MaxHalfMax: windowPx,
	})
	for _, p := range peaks {
		if int(p.X) < windowPx || int(p.Y) < windowPx ||
			int(p.X) >= im.W-windowPx || int(p.Y) >= im.H-windowPx {
			continue
		}
		if isPointDefect(im, p.X, p.Y) {
			continue
		}
		star, err := Measure(im, float64(p.X), float64(p.Y))
		if err != nil || star.SNR < o.MinSNR {
			continue
		}
		return star, nil
	}
	return Star{}, ErrNoStar
}

// Refind measures the star nearest an expected position.
//
// It searches only a neighbourhood, and deliberately does not fall back to the whole frame: a tracker
// that silently re-acquires a DIFFERENT star injects a step of tens of arcseconds into the middle of
// a run, and that step is then fitted as though the worm had done it.
//
// searchPx sizes that neighbourhood. It is a parameter rather than a constant because the two callers
// want opposite things: while tracking, the star barely moves and a tight search is what keeps a
// neighbour from being mistaken for it; while the drive is off for calibration, the star crosses
// tens of pixels between frames and a tight search would simply lose it. Passing 0 takes the tight
// default.
func Refind(im *fits.Image, expectX, expectY float64, searchPx int, o Options) (Star, error) {
	o = o.withDefaults()
	if searchPx <= 0 {
		searchPx = windowPx
	}
	x, y := int(math.Round(expectX)), int(math.Round(expectY))
	if x < windowPx || y < windowPx || x >= im.W-windowPx || y >= im.H-windowPx {
		return Star{}, ErrNoStar
	}
	px, py, ok := brightestIn(im, x, y, searchPx)
	if !ok || isPointDefect(im, px, py) {
		return Star{}, ErrNoStar
	}
	// The measuring box must stay on the sensor even when the search ran to its edge.
	if px < windowPx || py < windowPx || px >= im.W-windowPx || py >= im.H-windowPx {
		return Star{}, ErrNoStar
	}
	star, err := Measure(im, float64(px), float64(py))
	if err != nil {
		return Star{}, err
	}
	if star.SNR < o.MinSNR {
		return Star{}, ErrNoStar
	}
	return star, nil
}

// brightestIn finds the brightest pixel in a box.
func brightestIn(im *fits.Image, cx, cy, half int) (int, int, bool) {
	plane := im.Pix[0]
	best := float32(math.Inf(-1))
	bx, by, found := 0, 0, false
	for y := cy - half; y <= cy+half; y++ {
		if y < 0 || y >= im.H {
			continue
		}
		for x := cx - half; x <= cx+half; x++ {
			if x < 0 || x >= im.W {
				continue
			}
			if v := plane[y*im.W+x]; v > best {
				best, bx, by, found = v, x, y, true
			}
		}
	}
	return bx, by, found
}
