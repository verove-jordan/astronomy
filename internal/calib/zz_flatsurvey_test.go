package calib

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// TestZZFlatSurvey measures what a set of phone flats actually contains.
//
// A flat is only worth applying if the pattern in it belongs to the CAMERA. Two things masquerade as
// one here:
//
//   - Vignetting and dust are fixed in SENSOR coordinates, so rolling the phone about its optical axis
//     leaves them exactly where they were.
//   - The light source's own unevenness is fixed in the WORLD, so the same roll turns it under the
//     sensor by the same angle.
//
// That is the whole point of shooting the set at different rolls, and it is also the only way to tell
// the two apart. This reports, per frame, the radial falloff about the frame centre (the part a roll
// cannot move) and where the brightest region sits as an angle (the part it must move, if it belongs
// to the room rather than the lens).
//
//	ASTRO_FLAT_DIR=<abs input/IPHONE_FLAT> go test ./internal/calib/ -run TestZZFlatSurvey -v
func TestZZFlatSurvey(t *testing.T) {
	dir := os.Getenv("ASTRO_FLAT_DIR")
	if dir == "" {
		t.Skip("set ASTRO_FLAT_DIR to a folder of flat frames")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.DNG"))
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Skip("no DNGs")
	}
	tmp := t.TempDir()

	var stack [][]float64
	var frameSpan []float64
	var sw, sh int

	fmt.Printf("%-14s %6s %7s | %8s %8s %8s %8s | %7s %7s\n",
		"frame", "roll", "level", "r<0.05", "r~0.4", "r~0.7", "r~0.95", "asym%", "asymPA")
	for _, p := range paths {
		m := rawmeta.Read(p)
		f, _ := pointing.FromMeta(m)
		small := filepath.Join(tmp, filepath.Base(p)+".png")
		// A flat is smooth by nature, so a downscale measures it as well as the full frame and makes
		// twenty-one 48-megapixel files tractable.
		if err := rawconv.ThumbnailForStats(context.Background(), p, small, 256); err != nil {
			fmt.Printf("%-14s  could not develop: %v\n", filepath.Base(p), err)
			continue
		}
		g, w, h, err := readGray(small)
		if err != nil {
			fmt.Printf("%-14s  %v\n", filepath.Base(p), err)
			continue
		}
		prof, level := radialProfile(g, w, h)
		ax, apa := asymmetry(g, w, h)
		// Each frame normalised by its OWN level, so exposure and ISO differences cannot weight the
		// combine — they range over a factor of nearly three here.
		n := make([]float64, len(g))
		for i, v := range g {
			n[i] = v / level
		}
		stack, sw, sh = append(stack, n), w, h
		frameSpan = append(frameSpan, centreSpan(g, prof, level, w, h))
		fmt.Printf("%-14s %6.1f %7.4f | %8.3f %8.3f %8.3f %8.3f | %6.1f%% %6.0f\n",
			filepath.Base(p), f.RollDeg, level, prof(0.02), prof(0.4), prof(0.7), prof(0.95), 100*ax, apa)
		if out := os.Getenv("ASTRO_FLAT_RESIDUAL"); out != "" {
			// Divide the frame by its own radial model. What is left is everything the model cannot
			// explain: dust, a reflection of the phone's own back, the edge of the light source. A pure
			// vignette leaves flat grey.
			res := make([]float64, len(g))
			cx, cy := float64(w)/2, float64(h)/2
			maxR := math.Hypot(cx, cy)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					m := prof(math.Hypot(float64(x)-cx, float64(y)-cy) / maxR)
					if m > 0 {
						res[y*w+x] = g[y*w+x] / (m * level)
					}
				}
			}
			writeStretched(res, w, h, filepath.Join(out, "resid_"+filepath.Base(p)+".png"))
		}
		if os.Getenv("ASTRO_FLAT_PROFILE") != "" {
			fmt.Printf("    profile r=0.00..1.00:")
			for i := 0; i < 20; i++ {
				fmt.Printf(" %.3f", prof(float64(i)/20+0.001))
			}
			fmt.Println()
		}
	}

	if out := os.Getenv("ASTRO_FLAT_RESIDUAL"); out != "" && len(stack) > 2 {
		med := make([]float64, sw*sh)
		col := make([]float64, len(stack))
		for i := range med {
			for k := range stack {
				col[k] = stack[k][i]
			}
			sort.Float64s(col)
			med[i] = col[len(col)/2]
		}
		mprof, mlevel := radialProfile(med, sw, sh)
		cx, cy := float64(sw)/2, float64(sh)/2
		maxR := math.Hypot(cx, cy)
		mres := make([]float64, len(med))
		var peak, lo float64
		for y := 0; y < sh; y++ {
			for x := 0; x < sw; x++ {
				m := mprof(math.Hypot(float64(x)-cx, float64(y)-cy) / maxR)
				if m > 0 {
					v := med[y*sw+x] / (m * mlevel)
					mres[y*sw+x] = v
					if r := math.Hypot(float64(x)-cx, float64(y)-cy) / maxR; r < 0.45 {
						peak = math.Max(peak, v)
						if lo == 0 || v < lo {
							lo = v
						}
					}
				}
			}
		}
		sort.Float64s(frameSpan)
		fmt.Printf("\nsingle frames: centre residual spans %.2f%% (best) .. %.2f%% (median) .. %.2f%% (worst)\n",
			100*frameSpan[0], 100*frameSpan[len(frameSpan)/2], 100*frameSpan[len(frameSpan)-1])
		fmt.Printf("master flat from %d frames: residual inside r<0.45 spans %.4f..%.4f (%.2f%% peak-to-peak)\n",
			len(stack), lo, peak, 100*(peak-lo))

		// The radial-only model that actually gets used, fitted to these real frames.
		prof := RadialProfileOf(med, sw, sh, radialBins)
		v, err := FitRadialVignette(prof, 0.45)
		if err != nil {
			fmt.Println("radial fit failed:", err)
		} else {
			fmt.Printf("radial-only model: corner %.1f%% of centre, fit rms %.5f over r>%.2f\n",
				100*v.At(1), v.RMS, v.FitFrom)
			fmt.Printf("  model vs measured:")
			for _, r := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
				b := int(r * radialBins)
				fmt.Printf("  r=%.1f %.3f/%.3f", r, v.At(prof.MeanR[b]), prof.Level[b]/prof.Level[radialBins/4])
			}
			fmt.Println()
		}
		writeStretched(mres, sw, sh, filepath.Join(out, "master_residual.png"))
	}
}

// radialProfile returns the mean level in normalised-radius bins, divided by the centre, plus the
// centre level itself.
func radialProfile(g []float64, w, h int) (func(r float64) float64, float64) {
	cx, cy := float64(w)/2, float64(h)/2
	maxR := math.Hypot(cx, cy)
	const nb = radialBins
	sum := make([]float64, nb)
	cnt := make([]float64, nb)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)-cx, float64(y)-cy) / maxR
			b := int(r * nb)
			if b >= nb {
				b = nb - 1
			}
			sum[b] += g[y*w+x]
			cnt[b]++
		}
	}
	mean := make([]float64, nb)
	for i := range mean {
		if cnt[i] > 0 {
			mean[i] = sum[i] / cnt[i]
		}
	}
	// Normalise on an ANNULUS, not on the centre. The centre is exactly where a reflection of the
	// phone's own back would sit, and dividing by it would hide the very thing being looked for while
	// quietly depressing the whole profile.
	ref := (mean[4] + mean[5]) / 2 // r about 0.20 to 0.30
	// Interpolate between bin CENTRES. Nearest-bin lookup makes the model a staircase, and dividing a
	// smooth frame by a staircase draws concentric rings that look exactly like real optical structure
	// — which is what it did on the first attempt.
	return func(r float64) float64 {
		if ref == 0 {
			return 0
		}
		t := math.Min(math.Max(r, 0), 0.999)*nb - 0.5
		i := int(math.Floor(t))
		f := t - float64(i)
		switch {
		case i < 0:
			return mean[0] / ref
		case i >= nb-1:
			return mean[nb-1] / ref
		}
		return (mean[i]*(1-f) + mean[i+1]*f) / ref
	}, ref
}

// asymmetry measures how far the brightness centroid sits from the frame centre, as a fraction of the
// half-diagonal, and in which direction. A lens's vignetting is centred; a lamp off to one side is not.
func asymmetry(g []float64, w, h int) (frac, paDeg float64) {
	cx, cy := float64(w)/2, float64(h)/2
	var sx, sy, s float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := g[y*w+x]
			sx += v * (float64(x) - cx)
			sy += v * (float64(y) - cy)
			s += v
		}
	}
	if s == 0 {
		return 0, 0
	}
	dx, dy := sx/s, sy/s
	return math.Hypot(dx, dy) / math.Hypot(cx, cy), math.Mod(math.Atan2(dy, dx)*180/math.Pi+360, 360)
}

func readGray(path string) ([]float64, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	b := im.Bounds()
	w, h := b.Dx(), b.Dy()
	g := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, gg, bb, _ := im.At(b.Min.X+x, b.Min.Y+y).RGBA()
			g[y*w+x] = float64(r+gg+bb) / 3 / 65535
		}
	}
	var _ = image.Rect
	return g, w, h, nil
}

// writeStretched renders a residual around 1.0 with a hard stretch, so a one-percent structure is
// obvious rather than invisible.
func writeStretched(v []float64, w, h int, path string) {
	im := image.NewGray(image.Rect(0, 0, w, h))
	for i, x := range v {
		// 0.95..1.05 mapped across the full range.
		g := (x - 0.95) / 0.10
		im.Pix[i] = uint8(255 * math.Min(math.Max(g, 0), 1))
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, im)
}

// centreSpan is the peak-to-peak of a frame's residual inside r<0.45 — the region the phone's own
// reflection occupies.
func centreSpan(g []float64, prof func(float64) float64, level float64, w, h int) float64 {
	cx, cy := float64(w)/2, float64(h)/2
	maxR := math.Hypot(cx, cy)
	lo, hi := math.Inf(1), math.Inf(-1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := math.Hypot(float64(x)-cx, float64(y)-cy) / maxR
			if r >= 0.45 {
				continue
			}
			m := prof(r)
			if m <= 0 {
				continue
			}
			v := g[y*w+x] / (m * level)
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	return hi - lo
}

const radialBins = 20
