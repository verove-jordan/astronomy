// Per-star colour sampling on a linear RGB image, exported for the deep-sky star-quality analysis
// (internal/pipeline/starquality.go). It reuses the star-field-calibration detector so "what is a star"
// is defined once (starcal.go): bright, non-saturated local maxima, width- and separation-filtered. The
// measured colour is the background-subtracted aperture mean — the TRUE star colour, before any stretch.
package postprocess

import "github.com/verove-jordan/astronomy/internal/fits"

// StarColor is one detected star's background-subtracted mean colour in a small aperture (linear light).
type StarColor struct {
	X, Y    int
	R, G, B float64
}

// Sat returns the star's colour saturation (max−min)/max of its RGB channels — 0 for a grey/white star,
// higher for a strongly coloured one. Scale-invariant, so a highlight roll-off that scales all channels
// equally does not change it.
func (s StarColor) Sat() float64 {
	max, min := s.R, s.R
	for _, v := range [2]float64{s.G, s.B} {
		if v > max {
			max = v
		}
		if v < min {
			min = v
		}
	}
	if max <= 0 {
		return 0
	}
	return (max - min) / max
}

// StarColors detects up to maxStars bright, non-saturated stars in a linear RGB image and returns each
// star's background-subtracted mean colour in a small aperture. Returns nil for a non-RGB image. maxStars
// ≤ 0 keeps all detected stars (already capped at starCalMaxStars by the detector).
func StarColors(im *fits.Image, maxStars int) []StarColor {
	if im == nil || im.C != 3 {
		return nil
	}
	peaks := detectStars(lumaPlane(im), im.W, im.H)
	if maxStars > 0 && len(peaks) > maxStars {
		peaks = peaks[:maxStars]
	}
	bg := channelBackgrounds(im)
	out := make([]StarColor, 0, len(peaks))
	for _, p := range peaks {
		if mean, ok := apertureMean(im, bg, p.x, p.y); ok {
			out = append(out, StarColor{X: p.x, Y: p.y, R: mean[0], G: mean[1], B: mean[2]})
		}
	}
	return out
}

// apertureMean returns the background-subtracted mean per channel in a ±starCalWindow box around (px,py),
// and false when the aperture holds no positive signal.
func apertureMean(im *fits.Image, bg [3]float64, px, py int) ([3]float64, bool) {
	var sum [3]float64
	n := 0
	for dy := -starCalWindow; dy <= starCalWindow; dy++ {
		y := py + dy
		if y < 0 || y >= im.H {
			continue
		}
		for dx := -starCalWindow; dx <= starCalWindow; dx++ {
			x := px + dx
			if x < 0 || x >= im.W {
				continue
			}
			n++
			for c := 0; c < 3; c++ {
				if v := float64(im.Pix[c][y*im.W+x]) - bg[c]; v > 0 {
					sum[c] += v
				}
			}
		}
	}
	if n == 0 || (sum[0] <= 0 && sum[1] <= 0 && sum[2] <= 0) {
		return [3]float64{}, false
	}
	inv := 1.0 / float64(n)
	return [3]float64{sum[0] * inv, sum[1] * inv, sum[2] * inv}, true
}
