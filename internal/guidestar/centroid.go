package guidestar

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// The windowed centroid itself.

// centroidPasses is how many times the Gaussian window is re-centred. The first pass moves most of
// the way, the second essentially converges; a third is cheap insurance against a bad start.
const centroidPasses = 3

// Measure returns the sub-pixel position of the star near (px, py).
//
// The estimator is a Gaussian-weighted first moment, iterated. Weighting by a smooth function of
// distance — rather than by membership of a thresholded set — is the whole point: every pixel's
// contribution changes continuously as the star moves, so there is no discontinuity for the answer to
// snap to, and the measured position stays linear in the true one across a whole pixel.
func Measure(im *fits.Image, px, py float64) (Star, error) {
	x0, y0 := int(px)-windowPx, int(py)-windowPx
	x1, y1 := int(px)+windowPx, int(py)+windowPx
	if x0 < 0 || y0 < 0 || x1 >= im.W || y1 >= im.H {
		return Star{}, ErrNoStar
	}
	plane := im.Pix[0]
	bg, noise := windowBackground(plane, im.W, x0, y0, x1, y1)

	peak := float64(plane[int(py)*im.W+int(px)]) - bg
	if peak <= 0 {
		return Star{}, ErrNoStar
	}

	// The window's width follows the star: too narrow throws away signal and amplifies noise, too
	// wide lets the background dominate. Half the half-flux diameter is the usual compromise.
	hfd := halfFluxDiameter(plane, im.W, x0, y0, x1, y1, px, py, bg, noise)
	sigma := math.Max(hfd/2, 1.5)

	cx, cy := px, py
	var flux float64
	for pass := 0; pass < centroidPasses; pass++ {
		nx, ny, f, ok := weightedMoment(plane, im.W, x0, y0, x1, y1, cx, cy, bg, sigma)
		if !ok {
			return Star{}, ErrNoStar
		}
		cx, cy, flux = nx, ny, f
	}

	star := Star{X: cx, Y: cy, Peak: peak, Flux: flux, HFD: hfd}
	star.SNR = snr(peak, noise)
	return star, nil
}

// perfectSNR is what a star on a perfectly flat background scores. That only happens in synthetic
// frames, but it has to be a LARGE number rather than zero: reporting zero would make every caller's
// minimum-SNR check reject a flawless star.
const perfectSNR = 1e6

func snr(peak, noise float64) float64 {
	if noise <= 0 {
		return perfectSNR
	}
	return math.Min(peak/noise, perfectSNR)
}

// weightedMoment is one pass of the Gaussian-weighted first moment.
func weightedMoment(plane []float32, w, x0, y0, x1, y1 int, cx, cy, bg, sigma float64) (nx, ny, flux float64, ok bool) {
	twoSigmaSq := 2 * sigma * sigma
	var sum, sumX, sumY float64
	for y := y0; y <= y1; y++ {
		row := y * w
		dy := float64(y) - cy
		for x := x0; x <= x1; x++ {
			v := float64(plane[row+x]) - bg
			if v <= 0 {
				// Background pixels that happen to sit below the sky carry no information, and
				// including them as negative weight destabilises the moment. This is a floor at
				// zero, not a threshold on the star: it does not move as the star does.
				continue
			}
			dx := float64(x) - cx
			weight := math.Exp(-(dx*dx + dy*dy) / twoSigmaSq)
			wv := v * weight
			sum += wv
			sumX += wv * float64(x)
			sumY += wv * float64(y)
		}
	}
	if sum <= 0 {
		return 0, 0, 0, false
	}
	return sumX / sum, sumY / sum, sum, true
}

// halfFluxDiameter sizes the star so the window can be scaled to it.
func halfFluxDiameter(plane []float32, w, x0, y0, x1, y1 int, px, py, bg, noise float64) float64 {
	floor := bg + 3*noise
	var flux, weighted float64
	for y := y0; y <= y1; y++ {
		row := y * w
		for x := x0; x <= x1; x++ {
			v := float64(plane[row+x]) - bg
			if v <= 0 || float64(plane[row+x]) < floor {
				continue
			}
			d := math.Hypot(float64(x)-px, float64(y)-py)
			flux += v
			weighted += v * d
		}
	}
	if flux <= 0 {
		return 3
	}
	return 2 * weighted / flux
}

// windowBackground measures the sky from the window's border ring — local, so a gradient across the
// frame does not bias one part of the sensor against another. The spread comes from the median
// absolute deviation, which one stray star in the ring cannot drag the way a standard deviation
// would.
func windowBackground(plane []float32, w, x0, y0, x1, y1 int) (level, noise float64) {
	vals := make([]float64, 0, 2*(x1-x0+1)+2*(y1-y0+1))
	for x := x0; x <= x1; x++ {
		vals = append(vals, float64(plane[y0*w+x]), float64(plane[y1*w+x]))
	}
	for y := y0; y <= y1; y++ {
		vals = append(vals, float64(plane[y*w+x0]), float64(plane[y*w+x1]))
	}
	sort.Float64s(vals)
	level = vals[len(vals)/2]

	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - level)
	}
	sort.Float64s(dev)
	return level, 1.4826 * dev[len(dev)/2]
}

// neighbourFluxFloor is the least fraction of a peak's height its eight neighbours must carry. A real
// star spreads across several pixels because the optics and the atmosphere say so; a hot pixel does
// not. Every sensor has hundreds of them, they look like very sharp stars to a peak finder, and a
// tracker locked onto one would report a mount with no periodic error at all.
const neighbourFluxFloor = 0.15

func isPointDefect(im *fits.Image, px, py int) bool {
	if px < 2 || py < 2 || px >= im.W-2 || py >= im.H-2 {
		return true
	}
	plane := im.Pix[0]
	peak := float64(plane[py*im.W+px])
	bg, _ := windowBackground(plane, im.W, px-2, py-2, px+2, py+2)
	amplitude := peak - bg
	if amplitude <= 0 {
		return true
	}
	var neighbours float64
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			if v := float64(plane[(py+dy)*im.W+px+dx]) - bg; v > 0 {
				neighbours += v
			}
		}
	}
	return neighbours < neighbourFluxFloor*amplitude
}
