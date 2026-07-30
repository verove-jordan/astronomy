package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Noise-normalized band-pass detail metric. The previous ranking (Laplacian variance) rewards
// pixel noise as strongly as real structure: a noisier frame could outrank a sharper one, and
// the master-vs-best-frame acceptance gate compared images whose noise floors differ. This
// metric isolates the 2–4 px crater-scale band (box3 − box5), measures its variance over the
// lit disk, subtracts the analytically-known contribution of the frame's own high-frequency
// noise to that band, and normalizes by the disk's robust dynamic range² (scale-invariant).
const (
	// bandPassNoiseGain is ‖box3−box5‖²: the variance fraction a unit-variance iid noise
	// field keeps after the box3−box5 band-pass — 9·(1/9−1/25)² + 16·(1/25)² = 3600/50625.
	bandPassNoiseGain = 3600.0 / 50625.0
	// hfResidualGain is ‖δ−box3‖²: the variance fraction unit noise keeps in p−box3 (8/9),
	// so σ_noise² = σ_residual² / hfResidualGain.
	hfResidualGain = 8.0 / 9.0
	// detailMinLitPx guards degenerate inputs: below this many lit pixels the metric is 0.
	detailMinLitPx = 100
	// litErodePx shrinks the lit mask before the global metric: the band-pass kernel spans a
	// 5×5 window (chebyshev 2 ≡ manhattan ≤ 4), so pixels closer than this to sky or shadow
	// carry the limb/terminator STEP in their band energy — edge geometry, not surface detail.
	litErodePx = 4
)

// detailPlanes computes the shared per-frame inputs of the detail metric: the band-pass plane
// d = box3(p)−box5(p) over the first plane, the lit threshold (shared apDiskFrac recipe), the
// robust dynamic range, and the noise floor σ_n. Returns a nil plane on a degenerate frame.
func detailPlanes(im *fits.Image) (d []float32, thr float32, dyn, sigmaN float64) {
	p := im.Pix[0]
	bg := lowPercentile(p, 0.2)
	pk := lowPercentile(p, 0.999)
	if pk-bg <= 1e-9 {
		return nil, 0, 0, 0
	}
	thr = float32(bg + apDiskFrac*(pk-bg))
	box3 := comet.BoxBlur(p, im.W, im.H, 1)
	box5 := comet.BoxBlur(p, im.W, im.H, 2)
	sigmaN = noiseSigmaHF(p, box3, thr)
	d = box5 // reuse the larger blur's buffer for the band-pass plane
	for i := range d {
		d[i] = box3[i] - box5[i]
	}
	return d, thr, pk - bg, sigmaN
}

// noiseSigmaHF estimates the frame's per-pixel noise σ from the high-frequency residual
// p − box3 over lit pixels: a subsampled MAD, robust to the (sparse, structured) texture the
// residual also carries, corrected for the variance share the residual filter keeps.
func noiseSigmaHF(p, box3 []float32, thr float32) float64 {
	const maxSample = 100000
	step := 1
	if len(p) > maxSample {
		step = len(p) / maxSample
	}
	res := make([]float64, 0, maxSample+1)
	for i := 0; i < len(p); i += step {
		if p[i] > thr {
			res = append(res, float64(p[i]-box3[i]))
		}
	}
	if len(res) < detailMinLitPx {
		return 0
	}
	med := medianOf(res)
	for i, v := range res {
		res[i] = math.Abs(v - med)
	}
	mad := medianOf(res)
	return 1.4826 * mad / math.Sqrt(hfResidualGain)
}

// detailSNR is the noise-corrected band-pass detail of a frame's lit disk — the lucky-imaging
// ranking and acceptance metric. The lit mask is eroded by litErodePx so the limb/terminator
// steps contribute nothing; scaling every pixel by a constant leaves the score unchanged, so
// frames of different exposure/normalization rank on structure, not brightness or noise.
func detailSNR(im *fits.Image) float64 {
	if im.W < 5 || im.H < 5 {
		return 0
	}
	d, thr, dyn, sigmaN := detailPlanes(im)
	if d == nil {
		return 0
	}
	p := im.Pix[0]
	dark := make([]bool, len(p))
	for i := range p {
		dark[i] = p[i] <= thr
	}
	nearDark := imgops.BinaryDilation(dark, im.W, im.H, litErodePx)
	var sum, sum2 float64
	n := 0
	for i, v := range d {
		if nearDark[i] {
			continue
		}
		f := float64(v)
		sum += f
		sum2 += f * f
		n++
	}
	if n < detailMinLitPx {
		return 0
	}
	mean := sum / float64(n)
	v := sum2/float64(n) - mean*mean - bandPassNoiseGain*sigmaN*sigmaN
	if v <= 0 {
		return 0
	}
	return v / (dyn * dyn)
}

// regionDetail is the noise-corrected band-pass detail over one clamped rectangle of the
// shared plane d (from detailPlanes) — the per-AP-cell ranking. noiseVar is the whole frame's
// bandPassNoiseGain·σ_n² (per-cell noise estimates are too unstable) and dyn2 the frame's
// dynamic range²; both are constant per frame so cells rank consistently across frames.
func regionDetail(d []float32, w, h, x0, y0, rw, rh int, noiseVar, dyn2 float64) float64 {
	x1, y1 := x0+rw, y0+rh
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > w {
		x1 = w
	}
	if y1 > h {
		y1 = h
	}
	if x1-x0 < 3 || y1-y0 < 3 || dyn2 <= 0 {
		return 0
	}
	var sum, sum2 float64
	n := 0
	for y := y0; y < y1; y++ {
		row := y * w
		for x := x0; x < x1; x++ {
			f := float64(d[row+x])
			sum += f
			sum2 += f * f
			n++
		}
	}
	mean := sum / float64(n)
	v := sum2/float64(n) - mean*mean - noiseVar
	if v <= 0 {
		return 0
	}
	return v / dyn2
}
