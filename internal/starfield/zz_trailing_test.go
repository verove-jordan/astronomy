package starfield

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZTrailing measures how elongated the stars in a real sequence actually are.
//
// This exists to decide whether star-streak deconvolution is worth building at all. The plan
// estimates the sidereal drift at about 2.3 px at the celestial equator and zero at the pole, which
// is either a visible defect or nothing depending on how it compares with the stars' own width — and
// that comparison is a measurement, not an estimate.
//
// Elongation alone is not enough to convict drift: an optical aberration also elongates stars. Drift
// has a signature aberration does not, which is that its direction is COHERENT — every star in a
// small patch of sky trails the same way, because they all share the sky's rotation. So the position
// angles are checked for coherence as well.
//
//	ASTRO_TRAIL_SEQ=<abs .../01_seq> go test ./internal/starfield/ -run TestZZTrailing -v
func TestZZTrailing(t *testing.T) {
	dir := os.Getenv("ASTRO_TRAIL_SEQ")
	if dir == "" {
		t.Skip("set ASTRO_TRAIL_SEQ to a sequence directory holding light_*.fits")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "light_*.fits"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no light_*.fits under %s (%v)", dir, err)
	}
	sort.Strings(paths)
	if len(paths) > 4 {
		paths = paths[:4] // four frames is plenty to measure a systematic effect
	}
	for _, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		// Luminance: the shape is broadband and three planes would just triple the work.
		lum := make([]float32, im.W*im.H)
		for c := 0; c < im.C; c++ {
			for i := range lum {
				lum[i] += im.Pix[c][i]
			}
		}
		for i := range lum {
			lum[i] /= float32(im.C)
		}

		o := DefaultOptions()
		o.Max = 4000
		all := Detect(lum, im.W, im.H, o)

		// Only the BRIGHTEST stars may speak about shape, and this is not fussiness — it is the
		// difference between a real answer and a made-up one. Second moments need signal far from the
		// centre where the lever arm is longest and the noise is loudest, so this package's own
		// measurements say a round star reads 1.03 at peak SNR 660 and 1.19 by SNR 130, with the
		// noise splitting the two axes apart in a RANDOM direction. Measured over all 3828 detections
		// the median elongation was 1.47 with near-zero direction coherence, which is exactly what
		// noise looks like and nothing like what drift looks like. Take the brightest tenth instead.
		peaks := make([]float64, 0, len(all))
		for _, s := range all {
			peaks = append(peaks, s.Peak)
		}
		sort.Float64s(peaks)
		cut := peaks[int(0.90*float64(len(peaks)-1))]
		sat := peaks[len(peaks)-1] * 0.9
		var stars []Star
		for _, s := range all {
			if s.Peak >= cut && s.Peak < sat {
				stars = append(stars, s)
			}
		}
		fmt.Printf("%s: %d detections, %d bright enough to measure a shape\n",
			filepath.Base(p), len(all), len(stars))

		// Shape is the first thing to go as a star fades, so only well-measured, unsaturated stars
		// are allowed to speak. Elongation == 0 means UNMEASURED, not round.
		var el, fw []float64
		var sx, sy float64 // the doubled-angle mean, which is how you average an undirected angle
		n := 0
		for _, s := range stars {
			if s.Elongation <= 0 || s.FWHM <= 0 {
				continue
			}
			el = append(el, s.Elongation)
			fw = append(fw, s.FWHM)
			sx += math.Cos(2 * s.PADeg * math.Pi / 180)
			sy += math.Sin(2 * s.PADeg * math.Pi / 180)
			n++
		}
		if n < 20 {
			fmt.Printf("%s: only %d stars with a measurable shape\n", filepath.Base(p), n)
			continue
		}
		sort.Float64s(el)
		sort.Float64s(fw)
		q := func(v []float64, f float64) float64 { return v[int(f*float64(len(v)-1))] }
		// The resultant length of the doubled angles: 1 means every star trails the same way, 0 means
		// the directions are random. Drift is coherent; an aberration that varies across the field is
		// not, and noise certainly is not.
		coh := math.Hypot(sx, sy) / float64(n)
		meanPA := math.Mod(math.Atan2(sy, sx)/2*180/math.Pi+180, 180)
		fmt.Printf("%s: %d stars  FWHM p50 %.2f px  elongation p50 %.3f p90 %.3f  global coherence %.3f at %.0f deg\n",
			filepath.Base(p), n, q(fw, .5), q(el, .5), q(el, .9), coh, meanPA)

		// Global coherence cannot settle this on a 72-degree field: the drift direction is the local
		// parallel of declination, which curves right around near the pole, so genuine drift averages
		// away over the whole frame. Measure it in tiles instead, where the direction is near enough
		// constant that real drift MUST read as coherent.
		const tiles = 6
		var cohs []float64
		type acc struct {
			sx, sy float64
			n      int
		}
		grid := map[int]*acc{}
		for _, s := range stars {
			if s.Elongation <= 0 {
				continue
			}
			tx := int(s.X) * tiles / im.W
			ty := int(s.Y) * tiles / im.H
			k := ty*tiles + tx
			a := grid[k]
			if a == nil {
				a = &acc{}
				grid[k] = a
			}
			a.sx += math.Cos(2 * s.PADeg * math.Pi / 180)
			a.sy += math.Sin(2 * s.PADeg * math.Pi / 180)
			a.n++
		}
		for _, a := range grid {
			if a.n >= 20 {
				cohs = append(cohs, math.Hypot(a.sx, a.sy)/float64(a.n))
			}
		}
		sort.Float64s(cohs)

		// And the signature that tells an optical aberration from anything to do with the sky: is the
		// elongation aligned with the RADIUS from the frame centre? Coma and astigmatism are radial or
		// tangential by construction and grow with field angle; the sky knows nothing about where the
		// lens's axis is. +1 is purely radial, -1 purely tangential, 0 neither.
		cx, cy := float64(im.W)/2, float64(im.H)/2
		var radial, wsum float64
		var inner, outer []float64
		for _, s := range stars {
			if s.Elongation <= 0 {
				continue
			}
			r := math.Hypot(s.X-cx, s.Y-cy)
			if r < 50 {
				continue
			}
			rad := math.Atan2(s.Y-cy, s.X-cx) * 180 / math.Pi
			d := (s.PADeg - rad) * math.Pi / 180
			radial += math.Cos(2 * d)
			wsum++
			if r < 0.3*math.Hypot(cx, cy) {
				inner = append(inner, s.Elongation)
			} else if r > 0.8*math.Hypot(cx, cy) {
				outer = append(outer, s.Elongation)
			}
		}
		sort.Float64s(inner)
		sort.Float64s(outer)
		fmt.Printf("    tile coherence p50 %.3f p90 %.3f (%d tiles)   radial alignment %+.3f   elongation centre %.3f -> edge %.3f\n",
			q(cohs, .5), q(cohs, .9), len(cohs), radial/math.Max(wsum, 1), q(inner, .5), q(outer, .5))

		// The test that actually settles it: elongation against BRIGHTNESS. Noise in the second
		// moments splits the axes apart by an amount that shrinks as the signal grows, so a field of
		// genuinely round stars reads elongated at the faint end and converges towards 1 at the
		// bright end. A real smear does not care how bright the star is and plateaus instead. Whether
		// there is anything here to deconvolve is exactly the difference between those two shapes.
		byPeak := append([]Star(nil), all...)
		sort.Slice(byPeak, func(a, b int) bool { return byPeak[a].Peak > byPeak[b].Peak })
		fmt.Printf("    elongation by brightness:")
		for _, band := range [][2]float64{{0, 0.005}, {0.005, 0.02}, {0.02, 0.05}, {0.05, 0.15}, {0.15, 0.4}, {0.4, 1}} {
			lo, hi := int(band[0]*float64(len(byPeak))), int(band[1]*float64(len(byPeak)))
			var e []float64
			for _, s := range byPeak[lo:hi] {
				if s.Elongation > 0 {
					e = append(e, s.Elongation)
				}
			}
			if len(e) < 10 {
				continue
			}
			sort.Float64s(e)
			fmt.Printf("  top%.1f%%-%.0f%%:%.3f(n=%d)", band[0]*100, band[1]*100, q(e, .5), len(e))
		}
		fmt.Println()
	}
}
