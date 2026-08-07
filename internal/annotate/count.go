package annotate

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// countDetect tunes the shared detector for COUNTING rather than calibration sampling: the
// threshold stays noise-floor-relative (median + 5·MAD of the stack's own luma — a pure function
// of the pixels, so the count is stable run-to-run), the cap is removed, saturated bright-star
// cores are kept (their plateaus thin to one peak via MinSepPx), and the width filter is relaxed
// so bloomed bright stars still count while galaxy cores stay excluded.
var countDetect = postprocess.StarDetectOptions{
	Sigma:      5,
	MaxStars:   -1,
	MinSepPx:   6,
	SatLevel:   2, // ≥1 disables the saturation exclusion
	MaxHalfMax: 40,
	// The global 5σ floor is measured against the WHOLE frame's noise, which a bright nebula sits
	// far above — so inside M42 every filament and knot met it and got counted. Measured on the
	// real M42 master: 3848 peaks at 5σ alone, of which only ~1000 actually rise above their own
	// surroundings. Requiring 4 local sigmas as well cuts the texture without touching the stars
	// (the count converges with the independent width filter, which is the cross-check that the
	// survivors are point sources). Bright bloomed stars keep MaxHalfMax 40 so none is ever lost.
	MinLocalSigma: 4,
}

// maxPlottedStars caps how many detected positions ride in stars.json. The count itself is never
// capped; this only bounds the payload the browser has to hold and draw. 5000 is far beyond what any
// zoom level can legibly show, and the list is brightest-first so a truncated field loses only the
// faintest members.
const maxPlottedStars = 5000

// solved carries everything plotPoints can only know once the field has an astrometric solution:
// the magnitude zero point that turns instrumental flux into a magnitude, the per-peak catalogue
// identification, and the WCS that turns a pixel back into a line of sight. nil on an unsolved run,
// where the points are anonymous, magnitude-less and coordinate-less but still plottable.
type solved struct {
	wcs   fits.WCS
	zp    float64
	ident map[int]deepstars.Star
}

// plotPoints maps detected peaks (file grid) to final-image pixels for the overlay, keeping the
// brightest maxPlottedStars. The mapping is the same crop+row-flip the labels use, and it does NOT
// depend on the astrometric solution — so an unsolved run still plots its stars.
func plotPoints(im *fits.Image, m mapping, visible []postprocess.StarPeak, s *solved) []Point {
	n := len(visible)
	if n > maxPlottedStars {
		n = maxPlottedStars
	}
	out := make([]Point, 0, n)
	for i, p := range visible[:n] {
		x, y, _ := m.toFinal(float64(p.X), float64(p.Y))
		pt := Point{
			X:   int(math.Round(x)),
			Y:   int(math.Round(y)),
			Rpx: math.Max(1, float64(p.HalfWidth)/2),
			Hex: starHex(im, p.X, p.Y),
			Mag: noMagSentinel,
		}
		if s != nil {
			// Unrounded, so the line of sight is the peak's own rather than its display pixel's.
			pt.RADeg, pt.DecDeg = s.wcs.PixToSky(m.fileToWcs(m.fromFinal(x, y)))
			if f := starFlux(p); s.zp != 0 && f > 0 {
				pt.Mag = s.zp - 2.5*math.Log10(f)
			}
			if cs, ok := s.ident[i]; ok {
				pt.Star = starInfo(cs)
			}
		}
		out = append(out, pt)
	}
	return out
}

// starFlux is a crude total-flux proxy: peak height times the star's measured area. Peak height
// ALONE is not usable, because a bright star's core clips — every star above saturation reports the
// same peak, which compressed the whole magnitude scale into a few tenths. A clipped star spreads
// instead of climbing, so folding in its half-max area restores most of the ordering. This is a
// rough estimate, not aperture photometry, and it is labelled as such in the UI.
func starFlux(p postprocess.StarPeak) float64 {
	w := float64(p.HalfWidth)
	if w < 1 {
		w = 1
	}
	return float64(p.V) * w * w
}

// starHex samples a star's colour from the linear master and normalises it to full brightness. The
// RAW ratios are what carry the colour — the absolute level only says how bright the star is, and a
// dim-but-red star must still outline in red. Mono masters have no colour, so they get white.
func starHex(im *fits.Image, x, y int) string {
	if im == nil || im.C != 3 || x < 0 || y < 0 || x >= im.W || y >= im.H {
		return ""
	}
	i := y*im.W + x
	r, g, b := float64(im.Pix[0][i]), float64(im.Pix[1][i]), float64(im.Pix[2][i])
	max := math.Max(r, math.Max(g, b))
	if max <= 0 {
		return ""
	}
	// Lift toward white a little: a fully saturated hue on a dark image reads as a coloured smudge,
	// while a ring needs to stay visibly a ring.
	const floor = 0.45
	scale := func(v float64) int {
		u := floor + (1-floor)*(v/max)
		return int(math.Round(math.Min(1, math.Max(0, u)) * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", scale(r), scale(g), scale(b))
}

// magnitudeZeroPoint anchors instrumental brightness to real magnitudes using the catalogue stars
// that were actually identified in this frame: for each matched star, zp = V + 2.5·log10(peak), and
// the median of those is the frame's zero point. It needs a solved field and at least a few matches;
// 0 means "not anchored" and callers then report no magnitude rather than a made-up one.
func magnitudeZeroPoint(samples []float64) float64 {
	const minSamples = 3
	if len(samples) < minSamples {
		return 0
	}
	sort.Float64s(samples)
	return samples[len(samples)/2]
}

// matchDetect is countDetect WITHOUT the local-contrast gate, and it is deliberately the permissive
// one. Its peaks are used only to snap labels and to validate the flip, where a missed peak is far
// worse than a spurious one: a bright catalogue star buried in the Trapezium's glare has poor local
// contrast yet must still be matchable, and thinning the probe set would weaken the flip test that
// decides whether labels may be emitted at all. Measured on M42, gating this list dropped catalogue
// matches from 9/16 to 6/16 — enough to push a sparse field into validation_failed.
var matchDetect = func() postprocess.StarDetectOptions {
	o := countDetect
	o.MinLocalSigma = 0
	return o
}()

// detectAndCount returns the two peak sets the annotation needs, which are NOT the same set:
//   - all: permissive, for label snapping and flip validation;
//   - visible: local-contrast gated and inside the final crop, brightest first — what the badge
//     counts and the overlay plots.
//
// Two detector passes over the same pixels (~0.25 s) buys that separation; sharing one list would
// force a single threshold to serve two opposite requirements.
func detectAndCount(im *fits.Image, m mapping) (all, visible []postprocess.StarPeak) {
	all = postprocess.DetectStarPeaks(im, matchDetect)
	for _, p := range postprocess.DetectStarPeaks(im, countDetect) {
		if m.inWindow(p.X, p.Y) {
			visible = append(visible, p)
		}
	}
	return all, visible
}
