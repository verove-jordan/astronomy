// Star-protected, mean-preserving chroma noise reduction on the combined linear RGB.
//
// The original implementation blurred the chroma residual with ONE box pass — a flat square kernel
// whose impulse response painted a literal SQUARE of smeared colour around every very bright star
// (the star's concentrated wing chroma, spread uniformly over the (2r+1)² support). This rewrite
// keeps the same luminance identity (m=(R+G+B)/3 is byte-for-byte unchanged) and adds:
//
//   - Gaussian-shaped blurs (imgops.GaussianBlur, three box passes) — no flat kernel, no square
//     corners; σ = px/√3 matches the old single box's variance, so knob semantics survive.
//   - Winsorized residuals: the blur input is clipped to ±chromaClipSigmas·σc (the chroma noise
//     scale) and re-zeroed, so a bright star's wing chroma is bounded BEFORE it can spread — it can
//     never paint a plateau at any radius.
//   - SNR protection: pixels well above the sky keep their own chroma (smoothstep weights shared by
//     the three channels), and a dilated near-saturation core mask exempts star cores + wings.
//   - A second, much coarser background-only pass (Preset.ChromaBgSmoothPx) that flattens the large
//     green/brown chroma mottle at scales the fine pass cannot reach — genuine sky only.
package pipeline

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// chromaSmoothOpts carries the two chroma-NR radii in pixels (0 = that pass off).
type chromaSmoothOpts struct {
	FinePx int // Preset.ChromaSmoothPx — fine pass (residual colour patches)
	BgPx   int // Preset.ChromaBgSmoothPx — coarse background-only pass (large chroma mottle)
}

// Tuning constants for the chroma NR masks, centralised so retuning is one edit.
const (
	chromaMaskSigma    = 2.0     // σ of the luminance pre-blur that stabilises the SNR mask
	chromaClipSigmas   = 6.0     // residuals are winsorized to ±this many chroma sigmas before blurring
	chromaFineSNRLo    = 10.0    // fine pass: full smoothing below this SNR above sky…
	chromaFineSNRHi    = 30.0    // …none above it (bright star wings / galaxy cores keep their chroma)
	chromaBgSNRLo      = 2.0     // coarse pass: full smoothing below this SNR (genuine sky)…
	chromaBgSNRHi      = 6.0     // …none above it (any real signal keeps its colour)
	chromaCoreLevel    = 0.85    // near-saturation level marking bright star cores in the linear combine
	chromaStatsSamples = 200_000 // subsample size for the sky/noise statistics
)

// chromaSmoothRGB smooths ONLY the colour of a combined RGB linear FITS in place, preserving the
// per-pixel mean EXACTLY: m=(R+G+B)/3, c'=m+blend(c−m). The blend weights are shared by the three
// channels and every blur input sums to zero across channels, so (R'+G'+B')/3 stays byte-for-byte m —
// every bit of luminance/detail survives (in LRGB the L layer supplies detail anyway); only the mutual
// channel disagreements (colour noise/patches) are flattened. Bright-star wings and saturated cores
// keep their authentic chroma (see the file comment). Both radii ≤ 0 or a non-colour image → no-op.
// Soft-fail: returns a note and any error.
func chromaSmoothRGB(path string, o chromaSmoothOpts) (string, error) {
	if o.FinePx <= 0 && o.BgPx <= 0 {
		return "", nil
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("chroma smooth: read: %w", err)
	}
	if im.C < 3 {
		return "", nil // colour smoothing only makes sense on an RGB image
	}
	f, ok := buildChromaField(im, o)
	if !ok {
		return "chroma smooth skipped: degenerate image statistics", nil
	}
	// Reuse ONE residual buffer across the three channels (a 16MP master is ~64 MB/plane; the blurs
	// allocate their own outputs, so this scratch is the only per-channel plane we control).
	scratch := make([]float32, len(im.Pix[0]))
	for _, pix := range im.Pix[:3] {
		chromaSmoothChannel(pix, scratch, f, o)
	}
	if err := im.OverwriteData(path); err != nil {
		return "", fmt.Errorf("chroma smooth: write: %w", err)
	}
	return chromaSmoothNote(o), nil
}

// chromaField is the shared per-pixel context of one chroma-smooth run: the preserved luminance and
// its statistics (for the SNR protection weights), the winsorization zero-sum plane, and the core mask.
type chromaField struct {
	w, h       int
	mean       []float32 // (R+G+B)/3 — the preserved luminance
	lumS       []float32 // gaussian-stabilised luminance driving the SNR mask
	zero       []float32 // per-pixel mean of the clipped residuals — subtracted so Σ(blur input) ≡ 0
	core       []bool    // dilated near-saturation cores: fully exempt from replacement
	bg, sigmaL float64   // sky level + robust luminance sigma
	clip       float32   // winsorization bound (chromaClipSigmas·σc)
}

func buildChromaField(im *fits.Image, o chromaSmoothOpts) (*chromaField, bool) {
	f := &chromaField{w: im.W, h: im.H}
	r, g, b := im.Pix[0], im.Pix[1], im.Pix[2]
	f.mean = make([]float32, len(r))
	for i := range f.mean {
		f.mean[i] = (r[i] + g[i] + b[i]) / 3
	}
	var sigmaC float64
	f.bg, f.sigmaL, sigmaC = chromaStats(im, f.mean)
	if !(f.sigmaL > 0) || !(sigmaC > 0) {
		return nil, false // flat/degenerate statistics: the masks would divide by zero — skip, never guess
	}
	f.clip = float32(chromaClipSigmas * sigmaC)
	f.lumS = imgops.GaussianBlur(f.mean, im.W, im.H, chromaMaskSigma)
	f.core = chromaCoreMask(f.mean, im.W, im.H, max(o.FinePx, 4))
	f.zero = chromaZeroSum(im, f.mean, f.clip)
	return f, true
}

// chromaStats measures the sky level + robust sigma of the luminance plane and the pooled chroma
// noise sigma. All MAD-based, so real signal and star wings don't inflate them.
func chromaStats(im *fits.Image, mean []float32) (bg, sigmaL, sigmaC float64) {
	bg, sigmaL = medianMAD(imgops.Subsample(mean, chromaStatsSamples))
	step := 1
	if want := chromaStatsSamples / 3; len(mean) > want {
		step = len(mean) / want
	}
	res := make([]float32, 0, 3*(len(mean)/step+1))
	for _, pix := range im.Pix[:3] {
		for i := 0; i < len(pix); i += step {
			res = append(res, pix[i]-mean[i])
		}
	}
	_, sigmaC = medianMAD(res)
	return bg, sigmaL, sigmaC
}

// medianMAD returns the median and the MAD-based robust sigma (1.4826·MAD) of vals.
func medianMAD(vals []float32) (median, sigma float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	buf := make([]float64, len(vals))
	for i, v := range vals {
		buf[i] = float64(v)
	}
	sort.Float64s(buf)
	median = buf[len(buf)/2]
	for i, v := range buf {
		buf[i] = math.Abs(v - median)
	}
	sort.Float64s(buf)
	return median, 1.4826 * buf[len(buf)/2]
}

// chromaCoreMask marks near-saturation pixels (bright star cores in the linear combine), dilated a
// few px, so the entire replacement support around a core keeps its authentic chroma.
func chromaCoreMask(mean []float32, w, h, dilatePx int) []bool {
	mask := make([]bool, len(mean))
	found := false
	for i, v := range mean {
		if v > chromaCoreLevel {
			mask[i], found = true, true
		}
	}
	if !found {
		return mask
	}
	return imgops.BinaryDilation(mask, w, h, dilatePx)
}

// chromaZeroSum is the per-pixel mean of the three winsorized residuals. Clipping is nonlinear and
// would break the exact mean preservation, so the blur input is re-zeroed by subtracting this plane —
// the three blur inputs then sum to exactly 0 at every pixel, and by linearity so do their blurs.
func chromaZeroSum(im *fits.Image, mean []float32, clip float32) []float32 {
	z := make([]float32, len(mean))
	for _, pix := range im.Pix[:3] {
		for i := range z {
			z[i] += clipF32(pix[i]-mean[i], clip) / 3
		}
	}
	return z
}

// chromaSmoothChannel replaces one channel's chroma with its star-protected smoothed version, in
// place. scratch must be len(pix) and is overwritten (shared across the three channel calls).
func chromaSmoothChannel(pix, scratch []float32, f *chromaField, o chromaSmoothOpts) {
	for i := range scratch {
		scratch[i] = clipF32(pix[i]-f.mean[i], f.clip) - f.zero[i]
	}
	var smF, smB []float32
	if o.FinePx > 0 {
		smF = imgops.GaussianBlur(scratch, f.w, f.h, chromaSigmaPx(o.FinePx))
	}
	if o.BgPx > 0 {
		smB = coarseChroma(scratch, smF, f, o)
	}
	for i := range pix {
		if f.core[i] {
			continue // saturated core + dilated wings: authentic chroma, untouched
		}
		snr := (float64(f.lumS[i]) - f.bg) / f.sigmaL
		c := float64(pix[i] - f.mean[i]) // the UNclipped original chroma
		if smF != nil {
			c += (1 - smoothstep(chromaFineSNRLo, chromaFineSNRHi, snr)) * (float64(smF[i]) - c)
		}
		if smB != nil {
			c += (1 - smoothstep(chromaBgSNRLo, chromaBgSNRHi, snr)) * (float64(smB[i]) - c)
		}
		pix[i] = f.mean[i] + float32(c)
	}
}

// coarseChroma is the background pass's smoothing field: a further blur of the fine field when both
// passes run (gaussian composition, σB² = σF² + σrest²), else a direct blur of the residuals.
func coarseChroma(scratch, smF []float32, f *chromaField, o chromaSmoothOpts) []float32 {
	sigmaB := chromaSigmaPx(o.BgPx)
	if smF == nil {
		return imgops.GaussianBlur(scratch, f.w, f.h, sigmaB)
	}
	sigmaF := chromaSigmaPx(o.FinePx)
	rest := math.Sqrt(math.Max(0, sigmaB*sigmaB-sigmaF*sigmaF))
	return imgops.GaussianBlur(smF, f.w, f.h, rest)
}

// chromaSigmaPx converts a knob radius (px of the legacy single box pass) to a variance-matched
// gaussian sigma: a box of radius r has σ ≈ r/√3, so the knob keeps its old smoothing power.
func chromaSigmaPx(px int) float64 { return float64(px) / math.Sqrt(3) }

// chromaSmoothNote renders the run note for the enabled passes.
func chromaSmoothNote(o chromaSmoothOpts) string {
	switch {
	case o.FinePx > 0 && o.BgPx > 0:
		return fmt.Sprintf("chroma smoothed (mean-preserving, star-protected: gaussian %dpx + background %dpx)", o.FinePx, o.BgPx)
	case o.FinePx > 0:
		return fmt.Sprintf("chroma smoothed (mean-preserving, star-protected: gaussian %dpx)", o.FinePx)
	default:
		return fmt.Sprintf("chroma smoothed (mean-preserving, star-protected: background %dpx)", o.BgPx)
	}
}

// clipF32 clamps v to [−bound, bound].
func clipF32(v, bound float32) float32 {
	if v > bound {
		return bound
	}
	if v < -bound {
		return -bound
	}
	return v
}

// smoothstep is the Hermite S-curve: 0 for x ≤ a, 1 for x ≥ b, smooth in between (same shape as the
// unexported internal/noise helper; re-declared here rather than exporting it for one caller).
func smoothstep(a, b, x float64) float64 {
	if x <= a {
		return 0
	}
	if x >= b {
		return 1
	}
	t := (x - a) / (b - a)
	return t * t * (3 - 2*t)
}
