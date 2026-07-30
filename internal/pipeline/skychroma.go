// Post-stretch sky-chroma flatten.
//
// The linear pipeline (per-channel extraction, combined GraXpert/RBF pass, SPCC) leaves sub-percent
// per-channel background residuals — RBF ringing around a bright edge artifact, denoise chroma
// mottle, an imperfect gradient model. Invisible in linear, but the display stretch compresses the
// sky so hard that ±0.5% of linear chroma becomes a plainly visible red→teal band or a coloured
// disc in the final. No linear pass can guarantee neutrality at that precision, so this pass runs
// where the eye sees: on the STRETCHED RGB base, just before the GIMP composite. It measures the
// sky's chroma (c−m, m=(R+G+B)/3) on a coarse tile grid over sky pixels only, smooths it into a
// per-channel zero-sum field, and subtracts it with SNR-feathered object protection — the sky
// becomes neutral everywhere while galaxies, nebulae and stars keep their colour, and the
// per-pixel mean m (the luminance) is preserved exactly.
package pipeline

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

const (
	skyChromaSNRLo       = 2.0  // full flatten below this SNR above sky (genuine sky)…
	skyChromaSNRHi       = 6.0  // …none above it (objects keep their colour)
	skyChromaMaskSigma   = 2.0  // σ of the luminance pre-blur stabilising the SNR mask
	skyChromaMinSkyFrac  = 0.25 // a tile needs this fraction of sky pixels to measure chroma
	skyChromaSmoothTiles = 0.75 // gaussian σ (in tiles) smoothing the chroma field — tile medians are already low-noise
	// skyChromaMaxFrac caps the per-channel correction as a multiple of the sky level. The field only
	// ever contains MEASURED sky chroma (tile medians over sky pixels, object-protected), so the cap
	// is a backstop against degenerate statistics, not a working limit — a strong stray-light halo
	// carries chroma above 2× sky, and capping below that leaves a saturated residual disc (seen on
	// the M101 left-edge halo at 0.5).
	skyChromaMaxFrac = 2.5
	skyChromaSamples     = 200_000
	skyChromaMinGrid     = 8 // the refinement pass never measures below this pitch
)

// flattenSkyChroma neutralizes the large-scale sky chroma of a STRETCHED RGB FITS in place.
// tilePx is the measuring grid pitch (0 → no-op). Soft-fail: returns a note and any error.
func flattenSkyChroma(path string, tilePx int) (string, error) {
	if tilePx <= 0 {
		return "", nil
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("sky chroma flatten: read: %w", err)
	}
	if im.C < 3 {
		return "", nil // chroma only exists on an RGB image
	}
	mean := make([]float32, len(im.Pix[0]))
	// The protection mask keys on the MINIMUM channel, not the mean: a genuine object (galaxy,
	// star) is broadband — all three channels rise above the sky — while a stray-light blob is
	// chromatic (one channel only), so its min-channel stays at sky level and it gets neutralized
	// instead of protecting itself by lifting the local luminance.
	minc := make([]float32, len(im.Pix[0]))
	for i := range mean {
		r, g, b := im.Pix[0][i], im.Pix[1][i], im.Pix[2][i]
		mean[i] = (r + g + b) / 3
		minc[i] = min(r, g, b)
	}
	bg, sigma := medianMAD(imgops.Subsample(minc, skyChromaSamples))
	if !(sigma > 0) {
		return "sky chroma flatten skipped: degenerate statistics", nil
	}
	lumS := imgops.GaussianBlur(minc, im.W, im.H, skyChromaMaskSigma)

	// Two scales: the coarse grid takes out the broad bands, a half-pitch refinement pass takes out
	// disc-scale structure the coarse field's smoothing diluted.
	peak, applied := 0.0, false
	for _, grid := range []int{tilePx, max(tilePx/2, skyChromaMinGrid)} {
		p, ok := flattenSkyChromaPass(im, mean, lumS, bg, sigma, grid)
		if !ok {
			break // no measurable sky at this pitch — coarser result (if any) stands
		}
		applied = true
		peak = math.Max(peak, p)
		if grid <= skyChromaMinGrid {
			break // both entries resolved to the same pitch (tiny knob values)
		}
	}
	if !applied {
		return "sky chroma flatten skipped: not enough sky", nil
	}
	if err := im.OverwriteData(path); err != nil {
		return "", fmt.Errorf("sky chroma flatten: write: %w", err)
	}
	return fmt.Sprintf("sky chroma flattened (grid %dpx, peak %.1f%% of sky, objects protected)",
		tilePx, 100*peak/math.Max(bg, 1e-9)), nil
}

// flattenSkyChromaPass measures + subtracts one grid scale in place; mean must track im (it is
// unchanged by construction — the field is zero-sum across channels). Returns the field peak.
func flattenSkyChromaPass(im *fits.Image, mean, lumS []float32, bg, sigma float64, tilePx int) (float64, bool) {
	fields, ok := skyChromaTileFields(im, mean, lumS, bg, sigma, tilePx)
	if !ok {
		return 0, false
	}
	tw, th := tileDims(im.W, im.H, tilePx)
	peak := 0.0
	for c := range fields {
		fields[c] = imgops.GaussianBlur(fields[c], tw, th, skyChromaSmoothTiles)
		for _, v := range fields[c] {
			peak = math.Max(peak, math.Abs(float64(v)))
		}
	}
	applySkyChromaField(im, lumS, fields, bg, sigma, tilePx)
	return peak, true
}

func tileDims(w, h, tilePx int) (tw, th int) {
	return (w + tilePx - 1) / tilePx, (h + tilePx - 1) / tilePx
}

// skyChromaTileFields measures each channel's median chroma residual (pix−mean) per tile over SKY
// pixels only. Tiles without enough sky are filled from their neighbors, and every tile is
// re-zeroed across channels so the field can never shift the per-pixel mean.
func skyChromaTileFields(im *fits.Image, mean, lumS []float32, bg, sigma float64, tilePx int) ([3][]float32, bool) {
	tw, th := tileDims(im.W, im.H, tilePx)
	var fields [3][]float32
	for c := range fields {
		fields[c] = make([]float32, tw*th)
	}
	valid := make([]bool, tw*th)
	anyValid := false
	var res [3][]float64
	for ty := 0; ty < th; ty++ {
		for tx := 0; tx < tw; tx++ {
			for c := range res {
				res[c] = res[c][:0]
			}
			collectSkyResiduals(im, mean, lumS, bg, sigma, tilePx, tx, ty, &res)
			total := tileArea(im.W, im.H, tilePx, tx, ty)
			if len(res[0]) < int(skyChromaMinSkyFrac*float64(total)) {
				continue
			}
			t := ty*tw + tx
			valid[t], anyValid = true, true
			m := (median64(res[0]) + median64(res[1]) + median64(res[2])) / 3
			for c := range fields {
				fields[c][t] = float32(median64(res[c]) - m)
			}
		}
	}
	if !anyValid {
		return fields, false
	}
	fillInvalidTiles(&fields, valid, tw, th)
	return fields, true
}

func tileArea(w, h, tilePx, tx, ty int) int {
	x1 := min((tx+1)*tilePx, w)
	y1 := min((ty+1)*tilePx, h)
	return (x1 - tx*tilePx) * (y1 - ty*tilePx)
}

func collectSkyResiduals(im *fits.Image, mean, lumS []float32, bg, sigma float64, tilePx, tx, ty int, res *[3][]float64) {
	x0, y0 := tx*tilePx, ty*tilePx
	x1 := min(x0+tilePx, im.W)
	y1 := min(y0+tilePx, im.H)
	skyCut := float32(bg + skyChromaSNRLo*sigma)
	for y := y0; y < y1; y++ {
		row := y * im.W
		for x := x0; x < x1; x++ {
			i := row + x
			if lumS[i] >= skyCut {
				continue
			}
			for c := 0; c < 3; c++ {
				res[c] = append(res[c], float64(im.Pix[c][i]-mean[i]))
			}
		}
	}
}

// fillInvalidTiles propagates measured chroma into tiles that had no sky (object interiors) by
// repeated neighbor averaging — bounded, and guaranteed to terminate because at least one tile is
// valid and each pass converts every invalid tile that touches a valid one.
func fillInvalidTiles(fields *[3][]float32, valid []bool, tw, th int) {
	for pass := 0; pass < tw+th; pass++ {
		filled := false
		next := append([]bool(nil), valid...)
		for ty := 0; ty < th; ty++ {
			for tx := 0; tx < tw; tx++ {
				t := ty*tw + tx
				if valid[t] {
					continue
				}
				var sum [3]float64
				n := 0
				for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
					nx, ny := tx+d[0], ty+d[1]
					if nx < 0 || ny < 0 || nx >= tw || ny >= th || !valid[ny*tw+nx] {
						continue
					}
					for c := range sum {
						sum[c] += float64(fields[c][ny*tw+nx])
					}
					n++
				}
				if n == 0 {
					continue
				}
				for c := range sum {
					fields[c][t] = float32(sum[c] / float64(n))
				}
				next[t], filled = true, true
			}
		}
		copy(valid, next)
		if !filled {
			return
		}
	}
}

// applySkyChromaField subtracts the bilinearly-upsampled field with SNR-feathered protection: the
// same weight for all three channels, so the zero-sum field leaves m=(R+G+B)/3 untouched.
func applySkyChromaField(im *fits.Image, lumS []float32, fields [3][]float32, bg, sigma float64, tilePx int) {
	tw, th := tileDims(im.W, im.H, tilePx)
	fieldCap := float32(skyChromaMaxFrac * math.Max(bg, 1e-6))
	for y := 0; y < im.H; y++ {
		row := y * im.W
		for x := 0; x < im.W; x++ {
			i := row + x
			snr := (float64(lumS[i]) - bg) / sigma
			wgt := 1 - smoothstep(skyChromaSNRLo, skyChromaSNRHi, snr)
			if wgt <= 0 {
				continue
			}
			for c := 0; c < 3; c++ {
				f := clipF32(bilinearTile(fields[c], tw, th, tilePx, x, y), fieldCap)
				v := im.Pix[c][i] - float32(wgt)*f
				if v < 0 {
					v = 0
				}
				im.Pix[c][i] = v
			}
		}
	}
}

// median64 returns the median of vals (copy-sorted; empty → 0).
func median64(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	buf := append([]float64(nil), vals...)
	sort.Float64s(buf)
	return buf[len(buf)/2]
}

// bilinearTile samples the tile-grid field at a full-res pixel (tile centers are the knots).
func bilinearTile(field []float32, tw, th, tilePx int, x, y int) float32 {
	fx := (float64(x)+0.5)/float64(tilePx) - 0.5
	fy := (float64(y)+0.5)/float64(tilePx) - 0.5
	fx = math.Min(math.Max(fx, 0), float64(tw-1))
	fy = math.Min(math.Max(fy, 0), float64(th-1))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, tw-1), min(y0+1, th-1)
	dx, dy := float32(fx-float64(x0)), float32(fy-float64(y0))
	top := field[y0*tw+x0]*(1-dx) + field[y0*tw+x1]*dx
	bot := field[y1*tw+x0]*(1-dx) + field[y1*tw+x1]*dx
	return top*(1-dy) + bot*dy
}
