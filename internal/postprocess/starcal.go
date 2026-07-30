// Star-field photometric colour calibration — the REAL fallback when SPCC cannot run (no
// plate-solve, offline, unsolvable field). SPCC's neutralization fallback only equalizes the sky
// PEDESTAL per channel; it never touches per-channel GAIN, so an LRGB stack from a red-strong mono
// rig stays systematically warm. This pass estimates the channel gains from the field's own stars —
// but the anchor must be honest about what the median field star IS: a magnitude-limited field is
// dominated by K dwarfs/giants (B−V ≈ 0.85–1.0), noticeably WARMER than the Sun. Normalizing the
// median star to pure white (the old TargetRG/BG = 1.0) therefore over-suppresses R — task #316
// measured gains R=0.73/B=0.87 and the whole image (whose galaxies/sky do not follow the stellar
// ratio at all) tipped green with no downstream corrector. The default targets below anchor the
// median star warm instead, landing the calibration within a few percent of photometric.
package postprocess

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	starCalMinStars    = 20  // below this the estimate is noise — fall back to neutralization
	starCalMaxStars    = 500 // plenty for a stable median; caps the flux-summing cost
	starCalMinSep      = 8   // px between kept peaks (one detection per star)
	starCalWindow      = 5   // flux aperture radius (px)
	starCalSatLevel    = 0.9 // peaks above this are (near-)saturated — their ratios lie
	starCalMaxHalfMax  = 15  // wider-than-this at half max = galaxy knot / nebula blob, not a star
	starCalGainMin     = 0.5 // gain clamps: a correction outside this range means the
	starCalGainMax     = 2.0 // detection went wrong, not that the camera is 4x red-strong
	starCalDetectSigma = 8.0 // detection threshold above the luma background, in MAD-sigmas
	// Default anchor for the median field star: a K0–K2 dwarf/giant (B−V ≈ 0.85–1.0, vs the Sun's
	// 0.65), which through a typical mono-CMOS RGB filter set reads R/G ≈ 1.10 and B/G ≈ 0.90. The
	// white anchor (1.0/1.0) demonstrably over-corrected (#316: R×0.73 → green cast everywhere the
	// stellar ratio doesn't hold). Values validated against the Leo Triplet rerun.
	starCalTargetRG = 1.10
	starCalTargetBG = 0.90
)

// StarCalOptions tune the star-field calibration targets: the median star's R/G and B/G flux
// ratios are normalized to these values. ≤ 0 uses the warm defaults (starCalTargetRG/BG — the
// median field star is a K dwarf, not white).
type StarCalOptions struct {
	TargetRG float64
	TargetBG float64
}

// StarCalResult reports what the calibration measured and applied.
type StarCalResult struct {
	Applied      bool
	GainR, GainB float64
	Stars        int
}

// StarFieldCalibrate estimates per-channel gains from the stars of the linear RGB FITS at path and
// applies them in place: pix' = (pix - bg)·gain + neutral pedestal. Returns Applied=false (no
// error) when too few usable stars are found, so the caller can fall back to plain neutralization.
func StarFieldCalibrate(path string, opts StarCalOptions) (StarCalResult, error) {
	if opts.TargetRG <= 0 {
		opts.TargetRG = starCalTargetRG
	}
	if opts.TargetBG <= 0 {
		opts.TargetBG = starCalTargetBG
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		return StarCalResult{}, fmt.Errorf("star-field calibrate: %w", err)
	}
	if im.C != 3 {
		return StarCalResult{}, fmt.Errorf("star-field calibrate: need a 3-channel image, got %d", im.C)
	}

	bg := channelBackgrounds(im)
	lum := lumaPlane(im)
	stars := detectStars(lum, im.W, im.H)
	rg, bgr, usable := starFluxRatios(im, bg, stars)
	if usable < starCalMinStars {
		return StarCalResult{Stars: usable}, nil
	}

	gainR := clampGain(opts.TargetRG / rg)
	gainB := clampGain(opts.TargetBG / bgr)
	neutral := float32((bg[0] + bg[1] + bg[2]) / 3)
	gains := [3]float32{float32(gainR), 1, float32(gainB)}
	for c := 0; c < 3; c++ {
		pix, off := im.Pix[c], float32(bg[c])
		for i, v := range pix {
			nv := (v-off)*gains[c] + neutral
			if nv < 0 {
				nv = 0
			}
			pix[i] = nv
		}
	}
	if err := im.WriteFITS(path); err != nil {
		return StarCalResult{}, fmt.Errorf("star-field calibrate: write: %w", err)
	}
	return StarCalResult{Applied: true, GainR: gainR, GainB: gainB, Stars: usable}, nil
}

// channelBackgrounds estimates each channel's sky level with a sigma-clipped median over a strided
// sample (3 rounds at 2.5σ, σ from the MAD).
func channelBackgrounds(im *fits.Image) [3]float64 {
	var bg [3]float64
	step := sampleStep(im.W * im.H)
	for c := 0; c < 3; c++ {
		sample := make([]float64, 0, im.W*im.H/step+1)
		for i := 0; i < len(im.Pix[c]); i += step {
			sample = append(sample, float64(im.Pix[c][i]))
		}
		bg[c] = clippedMedian(sample, 3, 2.5)
	}
	return bg
}

// lumaPlane builds the detection luminance 0.25R + 0.5G + 0.25B.
func lumaPlane(im *fits.Image) []float32 {
	lum := make([]float32, im.W*im.H)
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	for i := range lum {
		lum[i] = 0.25*r[i] + 0.5*g[i] + 0.25*b[i]
	}
	return lum
}

// starFluxRatios sums each star's background-subtracted flux per channel in a small aperture and
// returns the median R/G and B/G ratios plus how many stars were usable.
func starFluxRatios(im *fits.Image, bg [3]float64, stars []starPeak) (rg, bgr float64, usable int) {
	var rgs, bgs []float64
	for _, p := range stars {
		var flux [3]float64
		for c := 0; c < 3; c++ {
			pix, off := im.Pix[c], float32(bg[c])
			for dy := -starCalWindow; dy <= starCalWindow; dy++ {
				y := p.y + dy
				if y < 0 || y >= im.H {
					continue
				}
				for dx := -starCalWindow; dx <= starCalWindow; dx++ {
					x := p.x + dx
					if x < 0 || x >= im.W {
						continue
					}
					if v := pix[y*im.W+x] - off; v > 0 {
						flux[c] += float64(v)
					}
				}
			}
		}
		if flux[0] <= 0 || flux[1] <= 0 || flux[2] <= 0 {
			continue
		}
		rgs = append(rgs, flux[0]/flux[1])
		bgs = append(bgs, flux[2]/flux[1])
	}
	if len(rgs) == 0 {
		return 0, 0, 0
	}
	return median(rgs), median(bgs), len(rgs)
}

func clampGain(g float64) float64 {
	if g < starCalGainMin {
		return starCalGainMin
	}
	if g > starCalGainMax {
		return starCalGainMax
	}
	return g
}

// sampleStep strides large planes down to ~1M samples for the robust statistics.
func sampleStep(n int) int {
	step := n / 1_000_000
	if step < 1 {
		step = 1
	}
	return step
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func medianMAD(v []float64) (med, mad float64) {
	med = median(v)
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - med)
	}
	return med, median(dev)
}

// clippedMedian iterates median/MAD clipping: values beyond sigma·1.4826·MAD are dropped each round.
func clippedMedian(v []float64, rounds int, sigma float64) float64 {
	cur := append([]float64(nil), v...) // never mutate the caller's slice via the in-place filter below
	for r := 0; r < rounds && len(cur) > 3; r++ {
		med, mad := medianMAD(cur)
		if mad == 0 {
			return med
		}
		lim := sigma * 1.4826 * mad
		next := cur[:0]
		for _, x := range cur {
			if math.Abs(x-med) <= lim {
				next = append(next, x)
			}
		}
		if len(next) == len(cur) {
			return med
		}
		cur = next
	}
	return median(cur)
}
