package comet

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Detection tuning. The blur radius is coma-scale: large enough to dissolve point-like stars (a few px)
// while preserving the extended coma; the window is the centroid box around the detected peak.
const (
	DefaultBlurRadius = 24
	DefaultWindow     = 40
	minPeakSigma      = 6.0 // the blurred peak must stand this many MADs above the median to be a comet
)

// Detect estimates the comet's centroid in a star-aligned frame. It heavily blurs the image (channel 0),
// which fades point-like stars but keeps the extended coma, finds the brightest blurred pixel, and
// returns the flux-weighted centroid in a window around it. ok is false when nothing stands out above the
// background (a flat field, or the comet is too faint to localize) so the caller can fall back.
func Detect(im *fits.Image, blurRadius, window int) (Point, bool) {
	if im == nil || im.W == 0 || im.H == 0 {
		return Point{}, false
	}
	if blurRadius < 1 {
		blurRadius = DefaultBlurRadius
	}
	if window < 1 {
		window = DefaultWindow
	}
	// Noise floor from the ORIGINAL plane (real per-pixel noise); blurring smooths it away, so the
	// blurred image's own MAD would be ~0 and useless as a threshold.
	bg, mad := medianMAD(im.Pix[0])
	blurred := boxBlur(im.Pix[0], im.W, im.H, blurRadius)
	peak, pi := maxOf(blurred)
	if peak <= bg+minPeakSigma*mad {
		return Point{}, false // no extended source clearly above the noise floor
	}
	return weightedCentroid(blurred, im.W, im.H, pi%im.W, pi/im.W, window, bg), true
}

// weightedCentroid is the flux-weighted center of the pixels in a (2*window+1) box around (px,py),
// weighting by brightness above the background so the diffuse coma is centered, not a stray hot pixel.
func weightedCentroid(v []float32, w, h, px, py, window int, bg float64) Point {
	var sw, sx, sy float64
	for y := py - window; y <= py+window; y++ {
		if y < 0 || y >= h {
			continue
		}
		for x := px - window; x <= px+window; x++ {
			if x < 0 || x >= w {
				continue
			}
			weight := float64(v[y*w+x]) - bg
			if weight <= 0 {
				continue
			}
			sw += weight
			sx += weight * float64(x)
			sy += weight * float64(y)
		}
	}
	if sw == 0 {
		return Point{X: float64(px), Y: float64(py)}
	}
	return Point{X: sx / sw, Y: sy / sw}
}

// BoxBlur returns a separable box-blurred copy of a single-channel plane (one horizontal then one
// vertical pass of radius r). Exported so callers (e.g. planetary AP alignment) can pre-blur a frame
// once and run many windowed correlations on it.
func BoxBlur(src []float32, w, h, r int) []float32 { return boxBlur(src, w, h, r) }

// boxBlur returns a separable box-blurred copy of a single-channel plane (one horizontal then one
// vertical pass of radius r). Edges average only the in-bounds neighbors.
func boxBlur(src []float32, w, h, r int) []float32 {
	tmp := blurAxis(src, w, h, r, true)
	return blurAxis(tmp, w, h, r, false)
}

func blurAxis(src []float32, w, h, r int, horizontal bool) []float32 {
	out := make([]float32, len(src))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float64
			var n int
			for k := -r; k <= r; k++ {
				xx, yy := x, y
				if horizontal {
					xx = x + k
				} else {
					yy = y + k
				}
				if xx < 0 || yy < 0 || xx >= w || yy >= h {
					continue
				}
				sum += float64(src[yy*w+xx])
				n++
			}
			out[y*w+x] = float32(sum / float64(n))
		}
	}
	return out
}

func maxOf(v []float32) (float64, int) {
	best, bi := math.Inf(-1), 0
	for i, x := range v {
		if float64(x) > best {
			best, bi = float64(x), i
		}
	}
	return best, bi
}

// medianMAD returns the median and median-absolute-deviation of a sampled subset (capped for speed).
func medianMAD(v []float32) (med, mad float64) {
	const maxSample = 100000
	step := 1
	if len(v) > maxSample {
		step = len(v) / maxSample
	}
	s := make([]float64, 0, len(v)/step+1)
	for i := 0; i < len(v); i += step {
		s = append(s, float64(v[i]))
	}
	if len(s) == 0 {
		return 0, 0
	}
	sort.Float64s(s)
	med = s[len(s)/2]
	for i := range s {
		s[i] = math.Abs(s[i] - med)
	}
	sort.Float64s(s)
	return med, s[len(s)/2]
}

// DetectFile reads a registered FITS frame and returns the comet centroid (with the default tuning).
func DetectFile(path string, blurRadius, window int) (Point, bool, error) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return Point{}, false, fmt.Errorf("comet detect read %s: %w", path, err)
	}
	p, ok := Detect(im, blurRadius, window)
	return p, ok, nil
}
