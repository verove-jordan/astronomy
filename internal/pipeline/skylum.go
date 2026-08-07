// Post-stretch sky LUMINANCE flatten — the brightness twin of skychroma.go.
//
// The linear background passes (per-channel GraXpert, the gentle subsky) leave a ~1% large-scale
// sky-level residual that the display stretch amplifies ~10× in relative terms — a grey-left /
// black-right sky. This pass removes exactly that class of artifact and nothing more: it fits a
// robust LOW-ORDER (quadratic) surface to the sky level in display space and subtracts its
// spatial variation. A quadratic can express a frame-scale glow but CANNOT paint local blotches,
// so the correction is smooth and natural by construction — no tile field, no exclusion-zone
// machinery. Objects drop out of the fit via iterative residual rejection and keep their contrast
// because the correction is a locally uniform shift (no feather — see applySkySurface). Runs on
// the stretched colour base, the L luminance layer, the mono-mode base and the mono side-outputs.
package pipeline

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

const (
	// Sky samples: per tile, the median level of pixels under a generous cut (the plane MAD is
	// dominated by the very gradient we remove, so the cut only has to exclude bright objects).
	skyLumSampleSigma = 3.0
	skyLumMinSkyFrac  = 0.5
	skyLumFitRejSigma = 2.5 // iterative fit rejection: object/edge-band tiles drop out here
	skyLumFitIters    = 3
	skyLumMinSamples  = 12 // fewer sky tiles than this → skip (no flatten beats a wrong one)
	// Correction cap as a fraction of the sky level — a BACKSTOP against a degenerate fit, sized
	// for real glows (M51's spans ~±95% of sky; the old 1.0 cap truncated the correction and left
	// visible residual bands where the glow exceeded it).
	skyLumMaxFrac = 2.5
)

// flattenSkyLuminance equalizes the large-scale sky LEVEL of a STRETCHED FITS in place (mono or
// RGB — every channel gets the same subtraction, so chroma is untouched by construction). tilePx
// is the sky-sampling pitch (0 → no-op).
func flattenSkyLuminance(path string, tilePx int) (string, error) {
	if tilePx <= 0 {
		return "", nil
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("sky luminance flatten: read: %w", err)
	}
	prot, lum := skyLumPlanes(im)
	planeBg, planeMAD := medianMAD(imgops.Subsample(prot, skyChromaSamples))
	if !(planeMAD > 0) {
		return "sky luminance flatten skipped: degenerate statistics", nil
	}
	protS := imgops.GaussianBlur(prot, im.W, im.H, skyChromaMaskSigma)

	us, vs, levels := skyLumSamples(im, lum, protS, float32(planeBg+skyLumSampleSigma*planeMAD), tilePx)
	if len(levels) < skyLumMinSamples {
		return "sky luminance flatten skipped: not enough sky", nil
	}
	surface, ok := fitSkySurface(us, vs, levels)
	if !ok {
		return "sky luminance flatten skipped: degenerate surface fit", nil
	}
	target, span := surfaceSpan(surface, us, vs)
	applySkySurface(im, surface, target, span)
	if err := im.OverwriteData(path); err != nil {
		return "", fmt.Errorf("sky luminance flatten: write: %w", err)
	}
	return fmt.Sprintf("sky luminance flattened (quadratic fit, span %.1f%% of sky, one level)",
		100*span/math.Max(target, 1e-9)), nil
}

// skyLumPlanes returns the object-protection plane (min channel for RGB — broadband objects only,
// see skychroma.go — the plane itself for mono) and the luminance plane the fit measures.
func skyLumPlanes(im *fits.Image) (prot, lum []float32) {
	if im.C < 3 {
		return im.Pix[0], im.Pix[0]
	}
	prot = make([]float32, len(im.Pix[0]))
	lum = make([]float32, len(im.Pix[0]))
	for i := range prot {
		r, g, b := im.Pix[0][i], im.Pix[1][i], im.Pix[2][i]
		lum[i] = (r + g + b) / 3
		prot[i] = min(r, g, b)
	}
	return prot, lum
}

// skyLumSamples measures the median sky level per tile (pixels under cut only; tiles mostly
// covered by an object are skipped — the robust fit rejects any leakage anyway). Coordinates are
// normalized to [-1,1].
func skyLumSamples(im *fits.Image, lum, protS []float32, cut float32, tilePx int) (us, vs, levels []float64) {
	tw, th := tileDims(im.W, im.H, tilePx)
	var vals []float64
	for ty := 0; ty < th; ty++ {
		for tx := 0; tx < tw; tx++ {
			x0, y0 := tx*tilePx, ty*tilePx
			x1, y1 := min(x0+tilePx, im.W), min(y0+tilePx, im.H)
			vals = vals[:0]
			for y := y0; y < y1; y++ {
				row := y * im.W
				for x := x0; x < x1; x++ {
					if protS[row+x] < cut {
						vals = append(vals, float64(lum[row+x]))
					}
				}
			}
			if len(vals) < int(skyLumMinSkyFrac*float64((x1-x0)*(y1-y0))) {
				continue
			}
			us = append(us, float64(x0+x1)/float64(im.W)-1)
			vs = append(vs, float64(y0+y1)/float64(im.H)-1)
			levels = append(levels, median64(vals))
		}
	}
	return us, vs, levels
}

// skySurface is the quadratic sky model c0 + c1·u + c2·v + c3·u² + c4·v² + c5·uv over [-1,1]².
type skySurface [6]float64

func (s skySurface) at(u, v float64) float64 {
	return s[0] + s[1]*u + s[2]*v + s[3]*u*u + s[4]*v*v + s[5]*u*v
}

// fitSkySurface least-squares fits the quadratic with iterative outlier rejection — sample tiles
// polluted by an object or a stacking-edge band sit far off the smooth sky and drop out.
func fitSkySurface(us, vs, levels []float64) (skySurface, bool) {
	keep := make([]bool, len(levels))
	for i := range keep {
		keep[i] = true
	}
	var s skySurface
	for iter := 0; iter < skyLumFitIters; iter++ {
		var ok bool
		s, ok = solveSurface(us, vs, levels, keep)
		if !ok {
			return s, false
		}
		res := make([]float64, 0, len(levels))
		for i := range levels {
			if keep[i] {
				res = append(res, levels[i]-s.at(us[i], vs[i]))
			}
		}
		sigma := math.Max(madSigmaOf(res), 1e-12)
		if iter == skyLumFitIters-1 {
			break
		}
		for i := range levels {
			if keep[i] && math.Abs(levels[i]-s.at(us[i], vs[i])) > skyLumFitRejSigma*sigma {
				keep[i] = false
			}
		}
	}
	return s, true
}

// solveSurface solves the 6×6 normal equations by Gaussian elimination with partial pivoting.
func solveSurface(us, vs, levels []float64, keep []bool) (skySurface, bool) {
	var m [6][7]float64
	basis := func(u, v float64) [6]float64 { return [6]float64{1, u, v, u * u, v * v, u * v} }
	n := 0
	for i := range levels {
		if !keep[i] {
			continue
		}
		n++
		b := basis(us[i], vs[i])
		for r := 0; r < 6; r++ {
			for c := 0; c < 6; c++ {
				m[r][c] += b[r] * b[c]
			}
			m[r][6] += b[r] * levels[i]
		}
	}
	if n < skyLumMinSamples {
		return skySurface{}, false
	}
	for col := 0; col < 6; col++ {
		pivot := col
		for r := col + 1; r < 6; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(m[pivot][col]) < 1e-12 {
			return skySurface{}, false
		}
		m[col], m[pivot] = m[pivot], m[col]
		for r := 0; r < 6; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for c := col; c <= 6; c++ {
				m[r][c] -= f * m[col][c]
			}
		}
	}
	var s skySurface
	for r := 0; r < 6; r++ {
		s[r] = m[r][6] / m[r][r]
	}
	return s, true
}

// surfaceSpan returns the level the sky equalizes TO — a LOW percentile (≈P10) of the fitted
// surface: the darkest genuine sky, i.e. what the frame looks like where the glow ISN'T. That is
// the only level-neutral choice: any higher target (the median was tried) sits mid-glow, and on a
// big glow the stretched sky then lands far above the level the finish curves were designed for —
// the whole frame washes grey. Equalization is SIGNED (applySkySurface): glow above the target
// comes down AND the few beyond-the-glow tiles the surface puts below it come up, so the whole
// sky lands on that one level instead of keeping a darker second zone. span is the peak absolute
// deviation from the target.
func surfaceSpan(s skySurface, us, vs []float64) (target, span float64) {
	at := make([]float64, len(us))
	for i := range us {
		at[i] = s.at(us[i], vs[i])
	}
	sorted := append([]float64(nil), at...)
	sort.Float64s(sorted)
	target = sorted[len(sorted)/10] // ≈P10: robust dark end, immune to a few dark outlier tiles
	for _, v := range at {
		span = math.Max(span, math.Abs(v-target))
	}
	return target, span
}

// applySkySurface shifts EVERY pixel of EVERY channel by the surface's local deviation from the
// target, exactly like Siril's subsky but signed (same value per channel → chroma untouched). No
// object feather on purpose: glow is ADDITIVE light over objects too, and shielding them leaves
// each one sitting on its glow pedestal as a grey aura — a locally uniform shift preserves every
// contrast instead (a lifted faint star rises WITH its sky, so no dark holes). Objects are
// excluded from the FIT (robust rejection), never from the correction.
func applySkySurface(im *fits.Image, s skySurface, target, span float64) {
	fieldCap := math.Min(span, skyLumMaxFrac*math.Max(target, 1e-6))
	channels := min(im.C, 3)
	for y := 0; y < im.H; y++ {
		v := 2*(float64(y)+0.5)/float64(im.H) - 1
		row := y * im.W
		for x := 0; x < im.W; x++ {
			local := s.at(2*(float64(x)+0.5)/float64(im.W)-1, v)
			f := float32(math.Max(-fieldCap, math.Min(fieldCap, local-target)))
			if f == 0 {
				continue
			}
			i := row + x
			for c := 0; c < channels; c++ {
				out := im.Pix[c][i] - f
				if out < 0 {
					out = 0
				} else if out > 1 {
					out = 1
				}
				im.Pix[c][i] = out
			}
		}
	}
}

// madSigmaOf is the MAD-based robust sigma of residuals (median absolute deviation about the
// median, Gaussian-scaled).
func madSigmaOf(res []float64) float64 {
	if len(res) == 0 {
		return 0
	}
	med := median64(res)
	abs := make([]float64, len(res))
	for i, v := range res {
		abs[i] = math.Abs(v - med)
	}
	return 1.4826 * median64(abs)
}
