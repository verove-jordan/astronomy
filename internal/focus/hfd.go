package focus

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// The HFD measurement itself.
//
// For each detected star: take a window around it, subtract the local background, find the
// flux-weighted centroid, then compute the flux-weighted mean radius. For a radially symmetric
// profile that mean radius IS the half-flux radius — which is why HFD costs two sums instead of a
// model fit, and why it cannot fail to converge while someone is turning a focuser.

// starWindowPx is the half-size of the box measured around each star. It must comfortably contain
// a defocused star: 30 px covers an HFD of ~40 px, which at f/7.4 with 3.8 µm pixels is over a
// millimetre out of focus — far more than anyone racks by hand.
const starWindowPx = 30

// measureStars returns the HFD of each usable star in the image.
func measureStars(im *fits.Image, o Options) []float64 {
	peaks := postprocess.DetectStarPeaks(im, postprocess.StarDetectOptions{
		Sigma: 6,
		// Detect far more candidates than are needed. Hot pixels are often BRIGHTER than the stars,
		// and the detector keeps the brightest first — with a tight budget the defects would fill
		// it and every real star would be discarded before it was ever measured.
		MaxStars: o.MaxStars*10 + 200,
		MinSepPx: starWindowPx, // stars closer than a window would contaminate each other
		SatLevel: 0.95,
		// The shared detector defaults to rejecting anything wider than 15 px at half maximum —
		// sensible when hunting stars in a finished image, catastrophic here: a badly defocused
		// star IS a wide blob, and rejecting it leaves the focus meter blind exactly when the user
		// needs it most. Allow blobs up to the measurement window.
		MaxHalfMax: 2 * starWindowPx,
	})
	out := make([]float64, 0, len(peaks))
	for _, p := range peaks {
		if isPointDefect(im, p.X, p.Y) {
			continue
		}
		if hfd, ok := hfdAt(im, p.X, p.Y); ok {
			out = append(out, hfd)
		}
		if len(out) >= o.MaxStars {
			break
		}
	}
	return out
}

// neighbourFluxFloor is the least fraction of a star's peak its eight neighbours must carry. A real
// star — even a sharp one — spreads across several pixels because the optics and the atmosphere say
// so. A hot pixel does not: it is one bright cell with nothing around it. Every sensor has hundreds
// of them, they look exactly like very sharp stars to a peak finder, and measuring them would peg
// the focus meter at "perfect" no matter how far out the focuser is.
const neighbourFluxFloor = 0.15

// isPointDefect reports whether a peak is an isolated bright pixel rather than a star.
func isPointDefect(im *fits.Image, px, py int) bool {
	// The 5×5 background ring below needs two pixels of margin, not one.
	if px < 2 || py < 2 || px >= im.W-2 || py >= im.H-2 {
		return true
	}
	plane := im.Pix[0]
	peak := float64(plane[py*im.W+px])
	// A local background from the 5×5 ring, so this survives a bright sky.
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

// hfdAt measures one star. ok is false when the window is clipped by the frame edge or holds no
// significant flux — both cases would otherwise drag the median around.
func hfdAt(im *fits.Image, px, py int) (float64, bool) {
	x0, y0 := px-starWindowPx, py-starWindowPx
	x1, y1 := px+starWindowPx, py+starWindowPx
	if x0 < 0 || y0 < 0 || x1 >= im.W || y1 >= im.H {
		return 0, false
	}
	plane := im.Pix[0]
	bg, noise := windowBackground(plane, im.W, x0, y0, x1, y1)

	// Only flux clearly ABOVE the noise counts. Including every pixel that merely beats the median
	// sounds harmless, but half the background qualifies by definition, and each of those pixels
	// carries a radius of up to the window size — which inflates HFD toward the window radius and,
	// worse, inflates it MORE for a sharp star than a bloated one, inverting the whole measurement.
	floor := bg + 3*noise

	// Centroid first: measuring radii from the brightest PIXEL rather than the true centre inflates
	// HFD by up to half a pixel, which matters when a good HFD is three.
	var sum, sumX, sumY float64
	for y := y0; y <= y1; y++ {
		row := y * im.W
		for x := x0; x <= x1; x++ {
			v := float64(plane[row+x]) - bg
			if v <= 0 || float64(plane[row+x]) < floor {
				continue
			}
			sum += v
			sumX += v * float64(x)
			sumY += v * float64(y)
		}
	}
	if sum <= 0 {
		return 0, false
	}
	cx, cy := sumX/sum, sumY/sum

	var flux, weighted float64
	for y := y0; y <= y1; y++ {
		row := y * im.W
		for x := x0; x <= x1; x++ {
			v := float64(plane[row+x]) - bg
			if v <= 0 || float64(plane[row+x]) < floor {
				continue
			}
			d := math.Hypot(float64(x)-cx, float64(y)-cy)
			if d > starWindowPx {
				continue // keep the measurement circular, not square
			}
			flux += v
			weighted += v * d
		}
	}
	if flux <= 0 {
		return 0, false
	}
	return 2 * weighted / flux, true
}

// windowBackground measures the sky under one star from the window's border ring — local, so a
// gradient across the frame does not bias one corner's stars against another's. It returns both the
// level and its noise (via the median absolute deviation, which a stray star in the ring cannot
// drag around the way a standard deviation would).
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
	return level, 1.4826 * dev[len(dev)/2] // MAD → Gaussian sigma
}

// cornerHFD measures the four corners separately. Equal corners mean the sensor is square to the
// optical axis; a consistent gradient across them is tilt — which no amount of focusing fixes, and
// which is invisible in a single centre reading.
func cornerHFD(pix []uint16, w, h int, o Options) []float64 {
	side := o.ROIPx / 2
	if side < 128 || side*2 >= w || side*2 >= h {
		return nil
	}
	corners := [][2]int{
		{0, 0}, {w - side, 0}, {0, h - side}, {w - side, h - side},
	}
	out := make([]float64, 0, 4)
	for _, c := range corners {
		im, _ := imageFromROI(pix, w, c[0], c[1], side, side)
		stars := measureStars(im, o)
		if len(stars) < 3 {
			return nil // an unmeasurable corner makes the whole comparison meaningless
		}
		sort.Float64s(stars)
		out = append(out, stars[len(stars)/2])
	}
	return out
}
