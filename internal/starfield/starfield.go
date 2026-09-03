// Package starfield finds stars in an image and measures their shape.
//
// The repo had no field star detector before this: guidestar centroids ONE known star for
// autoguiding, and the solar/comet/planetary detectors are each shaped around a single object.
// Everything else — registration, plate solving — was delegated to Siril. That is fine until the
// field is 72 degrees across, where Siril's solver will not converge and the mosaic needs its own
// measurements of where the stars are and what shape they came out.
//
// The shape measurement is not incidental. An untracked exposure trails its stars along the local
// direction of sky rotation, so the elongation and position angle measured here are a direct read of
// how far the sky moved during the frame — the correction the drift itself tells you how to make.
package starfield

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Star is one detection.
type Star struct {
	// X and Y are the flux-weighted centroid in pixels, with sub-pixel precision.
	X, Y float64
	// Flux is the background-subtracted sum over the measurement box.
	Flux float64
	// Peak is the brightest pixel, background-subtracted. Saturated stars read a flat peak and are
	// worth excluding from a fit, which is why it is reported.
	Peak float64
	// FWHM is the mean full width at half maximum in pixels, from the second moments. ZERO means the
	// shape could not be measured — see Elongation.
	FWHM float64
	// Elongation is major/minor axis, 1 for a round star. On an untracked frame it is the drift.
	//
	// ZERO means UNMEASURED, not round. Second moments need signal far from the centre, where the
	// lever arm is longest and the noise therefore loudest, so shape is the first thing to go as a
	// star gets fainter. Measured against synthetic stars (background noise 3 ADU, sigma 2 px): at
	// peak SNR 660 a true 2.0 reads 2.02 and a round star 1.03; by SNR 130 that is 2.20 and 1.19
	// (noise splits the two axes apart); below SNR ~30 the moment matrix stops being positive
	// definite altogether. Filter on Elongation > 0 and prefer the brightest unsaturated stars.
	Elongation float64
	// PADeg is the major axis angle, degrees counter-clockwise from the +x axis, in [0,180). Only
	// meaningful when Elongation is above 1 — a round star has no major axis.
	PADeg float64
}

// Options tune detection. The zero value is not usable; call DefaultOptions.
type Options struct {
	// Sigma is the detection threshold above the background, in units of the noise.
	Sigma float64
	// BoxRadius is the half-width of the measurement window, in pixels. It must comfortably contain
	// a trailed star: too small truncates the flux and biases the shape towards round.
	BoxRadius int
	// MinSeparation rejects a detection sitting closer than this to a brighter one, which is what
	// stops one bright star being reported several times from its own shoulders.
	MinSeparation float64
	// Max caps how many are returned, brightest first. 0 means no cap.
	Max int
}

// DefaultOptions detect comfortably-above-noise stars on a wide-field frame.
func DefaultOptions() Options {
	return Options{Sigma: 5, BoxRadius: 6, MinSeparation: 6, Max: 5000}
}

// backgroundSamples bounds the subsample the background and noise are estimated from.
const backgroundSamples = 200000

// Detect finds stars in a single plane, brightest first.
func Detect(plane []float32, w, h int, o Options) []Star {
	if w <= 0 || h <= 0 || len(plane) != w*h || o.BoxRadius < 1 {
		return nil
	}
	lb := newLocalBackground(plane, w, h)
	if lb.medianNoise() <= 0 {
		return nil
	}

	stars := make([]Star, 0, 256)
	for y := o.BoxRadius; y < h-o.BoxRadius; y++ {
		for x := o.BoxRadius; x < w-o.BoxRadius; x++ {
			v := float64(plane[y*w+x])
			bg, noise := lb.sample(float64(x), float64(y), w, h)
			if noise <= 0 || v < bg+o.Sigma*noise || !isLocalMax(plane, w, x, y, v) {
				continue
			}
			if s, ok := measure(plane, w, x, y, bg, o.BoxRadius); ok {
				stars = append(stars, s)
			}
		}
	}

	sort.Slice(stars, func(i, j int) bool { return stars[i].Flux > stars[j].Flux })
	stars = dedupe(stars, o.MinSeparation)
	if o.Max > 0 && len(stars) > o.Max {
		stars = stars[:o.Max]
	}
	return stars
}

// Background returns the plane's robust level and noise (median and MAD-derived sigma), estimated
// from a subsample so a 12-megapixel frame does not pay for a full sort.
func Background(plane []float32) (bg, noise float64) {
	samp := imgops.Subsample(plane, backgroundSamples)
	if len(samp) == 0 {
		return 0, 0
	}
	bg = imgops.Percentile(samp, 50)
	dev := make([]float32, len(samp))
	for i, v := range samp {
		dev[i] = float32(math.Abs(float64(v) - bg))
	}
	// 1.4826 converts a median absolute deviation into a Gaussian sigma.
	return bg, 1.4826 * imgops.Percentile(dev, 50)
}

// isLocalMax reports whether (x,y) is at least as bright as its eight neighbours. The >= on one side
// and > on the other breaks ties on a flat saturated core so it is claimed exactly once.
func isLocalMax(plane []float32, w, x, y int, v float64) bool {
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			n := float64(plane[(y+dy)*w+x+dx])
			if n > v || (n == v && (dy < 0 || (dy == 0 && dx < 0))) {
				return false
			}
		}
	}
	return true
}

// measure computes the centroid, flux and second moments over the box around a peak. ok is false
// when the box holds no net signal.
//
// Every pixel in the box contributes, including those that came out BELOW the background. Clipping
// them looks tidier and is wrong: it truncates the faint wings, and it truncates the long axis of an
// elongated star harder than the short one, so a trail measures rounder than it is (a 2.0
// elongation read back as 1.8). The negative excursions are noise about zero and cancel, leaving an
// unbiased estimate at the cost of a little variance.
func measure(plane []float32, w, cx, cy int, bg float64, r int) (Star, bool) {
	var sum, sx, sy, peak float64
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			v := float64(plane[(cy+dy)*w+cx+dx]) - bg
			sum += v
			sx += v * float64(cx+dx)
			sy += v * float64(cy+dy)
			if v > peak {
				peak = v
			}
		}
	}
	if sum <= 0 {
		return Star{}, false
	}
	s := Star{X: sx / sum, Y: sy / sum, Flux: sum, Peak: peak}

	// Second moments about the centroid give the shape.
	var mxx, myy, mxy float64
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			v := float64(plane[(cy+dy)*w+cx+dx]) - bg
			ex, ey := float64(cx+dx)-s.X, float64(cy+dy)-s.Y
			mxx += v * ex * ex
			myy += v * ey * ey
			mxy += v * ex * ey
		}
	}
	mxx, myy, mxy = mxx/sum, myy/sum, mxy/sum
	s.FWHM, s.Elongation, s.PADeg = shapeFromMoments(mxx, myy, mxy)
	return s, true
}

// shapeFromMoments turns the second moments into the axes of the equivalent Gaussian.
func shapeFromMoments(mxx, myy, mxy float64) (fwhm, elongation, paDeg float64) {
	// Eigenvalues of [[mxx,mxy],[mxy,myy]] are the variances along the principal axes.
	half := (mxx + myy) / 2
	diff := math.Sqrt(math.Max(0, ((mxx-myy)/2)*((mxx-myy)/2)+mxy*mxy))
	major, minor := half+diff, half-diff
	// A non-positive minor axis means noise has overwhelmed the moments. Report that as unmeasured
	// rather than clamping the ratio to 1 — "exactly round" is a confident claim, and it would be a
	// fabricated one.
	if major <= 0 || minor <= 0 {
		return 0, 0, 0
	}
	// 2*sqrt(2*ln2) converts a Gaussian sigma to its full width at half maximum.
	const sigmaToFWHM = 2.3548200450309493
	fwhm = sigmaToFWHM * math.Sqrt((major+minor)/2)
	elongation = math.Sqrt(major / minor)
	paDeg = math.Mod(0.5*math.Atan2(2*mxy, mxx-myy)*180/math.Pi+180, 180)
	return fwhm, elongation, paDeg
}

// dedupe drops any detection closer than minSep to an already-kept brighter one. Input must be
// sorted brightest first.
func dedupe(stars []Star, minSep float64) []Star {
	if minSep <= 0 {
		return stars
	}
	kept := make([]Star, 0, len(stars))
	minSep2 := minSep * minSep
	for _, s := range stars {
		tooClose := false
		for _, k := range kept {
			dx, dy := s.X-k.X, s.Y-k.Y
			if dx*dx+dy*dy < minSep2 {
				tooClose = true
				break
			}
		}
		if !tooClose {
			kept = append(kept, s)
		}
	}
	return kept
}
