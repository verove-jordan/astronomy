// Star-peak detection shared by the star-field colour calibration (starcal.go) and the run
// annotation (star counting / label matching): bright 8-neighbour local maxima on a luminance
// plane, thresholded against the plane's own median + MAD noise floor, width-filtered to exclude
// extended blobs (galaxy cores, nebula knots) and greedily thinned so each star yields one peak.
package postprocess

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// StarDetectOptions tune the shared star-peak detector. The zero value reproduces the
// star-field-calibration defaults (the starCal* constants in starcal.go) exactly.
type StarDetectOptions struct {
	Sigma      float64 // detection threshold in MAD-sigmas above the background (0 → 8)
	MaxStars   int     // brightest-first cap (0 → 500; negative → uncapped)
	MinSepPx   int     // greedy separation between kept peaks (0 → 8)
	SatLevel   float32 // exclude peaks at or above this level (0 → 0.9; ≥1 → keep saturated peaks)
	MaxHalfMax int     // reject blobs wider than this at half max (0 → 15)
	// MinLocalSigma additionally requires a peak to rise this many LOCAL MAD-sigmas above the median
	// of its own surrounding annulus. Sigma alone measures against the whole frame's noise floor,
	// which is meaningless inside a bright nebula: there, faint texture and knots sit far above the
	// global threshold and get counted as stars. 0 (the default) disables the test, so every existing
	// caller is byte-identical.
	MinLocalSigma float64
}

func (o StarDetectOptions) withDefaults() StarDetectOptions {
	if o.Sigma <= 0 {
		o.Sigma = starCalDetectSigma
	}
	if o.MaxStars == 0 {
		o.MaxStars = starCalMaxStars
	}
	if o.MinSepPx <= 0 {
		o.MinSepPx = starCalMinSep
	}
	if o.SatLevel <= 0 {
		o.SatLevel = starCalSatLevel
	}
	if o.MaxHalfMax <= 0 {
		o.MaxHalfMax = starCalMaxHalfMax
	}
	return o
}

// StarPeak is one detected star: 0-based pixel coordinates in the image's file row order plus the
// peak's luminance value.
type StarPeak struct {
	X, Y int
	V    float32
	// HalfWidth is the star's measured extent at half maximum, in pixels — the same quantity the
	// width filter already computes, kept instead of discarded so callers can size a marker to the
	// star rather than drawing every star the same. Roughly the FWHM.
	HalfWidth int
}

// DetectStarPeaks detects star peaks on im's luminance (3-channel image) or its single plane
// (mono). Images with any other channel count yield nil.
func DetectStarPeaks(im *fits.Image, o StarDetectOptions) []StarPeak {
	if im == nil {
		return nil
	}
	var lum []float32
	switch im.C {
	case 1:
		lum = im.Pix[0]
	case 3:
		lum = lumaPlane(im)
	default:
		return nil
	}
	peaks := detectStarsOpts(lum, im.W, im.H, o)
	out := make([]StarPeak, len(peaks))
	for i, p := range peaks {
		out[i] = StarPeak{X: p.x, Y: p.y, V: p.v, HalfWidth: p.hw}
	}
	return out
}

type starPeak struct {
	x, y int
	v    float32
	hw   int
}

// detectStars finds bright local maxima with the star-field-calibration defaults (see
// detectStarsOpts; kept so starcal.go/starcolors.go read as before).
func detectStars(lum []float32, w, h int) []starPeak {
	return detectStarsOpts(lum, w, h, StarDetectOptions{})
}

// detectStarsOpts finds bright local maxima in the luma plane: above bg + Sigma·σ(MAD),
// 8-neighbour maxima, saturation- and width-filtered, greedily thinned to MinSepPx and capped at
// MaxStars (brightest first).
func detectStarsOpts(lum []float32, w, h int, o StarDetectOptions) []starPeak {
	o = o.withDefaults()
	step := sampleStep(len(lum))
	sample := make([]float64, 0, len(lum)/step+1)
	for i := 0; i < len(lum); i += step {
		sample = append(sample, float64(lum[i]))
	}
	med, mad := medianMAD(sample)
	thr := float32(med + o.Sigma*1.4826*mad)

	var peaks []starPeak
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			v := lum[row+x]
			if v <= thr || (o.SatLevel < 1 && v >= o.SatLevel) {
				continue
			}
			if v < lum[row+x-1] || v < lum[row+x+1] ||
				v < lum[row-w+x-1] || v < lum[row-w+x] || v < lum[row-w+x+1] ||
				v < lum[row+w+x-1] || v < lum[row+w+x] || v < lum[row+w+x+1] {
				continue
			}
			hw := halfMaxWidth(lum, w, h, x, y, v, float32(med), o.MaxHalfMax)
			if hw > o.MaxHalfMax {
				continue // extended blob (galaxy core, nebula knot), not a star
			}
			if o.MinLocalSigma > 0 && localSigma(lum, w, h, x, y, v) < o.MinLocalSigma {
				continue // rises above the global noise floor, but not above its own surroundings
			}
			peaks = append(peaks, starPeak{x, y, v, hw})
		}
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].v > peaks[j].v })
	kept := peaks[:0]
	minSep2 := o.MinSepPx * o.MinSepPx
	for _, p := range peaks {
		ok := true
		for _, k := range kept {
			dx, dy := p.x-k.x, p.y-k.y
			if dx*dx+dy*dy < minSep2 {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, p)
			if o.MaxStars > 0 && len(kept) >= o.MaxStars {
				break
			}
		}
	}
	return kept
}

// Local-contrast annulus: far enough out to clear a star's own wings, tight enough to still sample
// the structure the star sits on (nebula filament, galaxy arm) rather than the distant sky.
const (
	localRingInner = 6
	localRingOuter = 12
)

// localSigma measures how far a peak stands above its OWN surroundings: (peak − median) / MAD of an
// annulus around it. This is the test that separates a star from a bright patch of nebula — inside
// M42 the global 5σ floor is met by texture everywhere, while a real star still spikes above the
// filament it sits on.
//
// The two degenerate cases both mean "no evidence to reject", so both PASS rather than fail: an
// annulus with no measurable scatter (a perfectly flat background — synthetic frames, or a masked
// region) makes any rise above it infinitely significant, and an annulus clipped by the image edge
// cannot be judged at all. Failing those would silently drop real stars, which is the one thing a
// star counter must not do.
func localSigma(lum []float32, w, h, px, py int, peak float32) float64 {
	var buf [(2*localRingOuter + 1) * (2*localRingOuter + 1)]float64
	vals := buf[:0]
	for dy := -localRingOuter; dy <= localRingOuter; dy++ {
		y := py + dy
		if y < 0 || y >= h {
			continue
		}
		for dx := -localRingOuter; dx <= localRingOuter; dx++ {
			d2 := dx*dx + dy*dy
			if d2 < localRingInner*localRingInner || d2 > localRingOuter*localRingOuter {
				continue
			}
			x := px + dx
			if x < 0 || x >= w {
				continue
			}
			vals = append(vals, float64(lum[y*w+x]))
		}
	}
	if len(vals) < 16 {
		return math.Inf(1) // annulus clipped by the frame edge — unjudgeable, so not rejected
	}
	sort.Float64s(vals)
	med := vals[len(vals)/2]
	for i := range vals {
		vals[i] = math.Abs(vals[i] - med)
	}
	sort.Float64s(vals)
	mad := vals[len(vals)/2] * 1.4826
	if mad <= 0 {
		if float64(peak) > med {
			return math.Inf(1) // flat surroundings: rising above them at all is unambiguous
		}
		return 0
	}
	return (float64(peak) - med) / mad
}

// halfMaxWidth measures the larger of the horizontal/vertical extents where the profile stays
// above half the peak's background-subtracted height, scanning at most maxHalfMax+1 px per side.
func halfMaxWidth(lum []float32, w, h, px, py int, peak, bg float32, maxHalfMax int) int {
	half := bg + (peak-bg)/2
	width := func(dx, dy int) int {
		n := 0
		for s := 1; s <= maxHalfMax+1; s++ {
			x, y := px+s*dx, py+s*dy
			if x < 0 || x >= w || y < 0 || y >= h || lum[y*w+x] < half {
				break
			}
			n++
		}
		return n
	}
	hw := width(-1, 0) + width(1, 0)
	vw := width(0, -1) + width(0, 1)
	if vw > hw {
		return vw
	}
	return hw
}
