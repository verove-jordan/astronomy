package planetary

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// The lit lunar surface spans a wide illumination range — the bright limb sits 2–3× above the
// terminator zone — so one global stretch cannot show terminator relief without pushing the limb
// band into a burnt, detail-less white. Limb balance separates the SMOOTH illumination field from
// crater-scale detail and compresses only the field's top: local contrast is untouched, so the limb
// keeps its craters while the terminator keeps its depth. One gain field is computed from the
// luminance and applied identically to every channel, so colour ratios are exactly preserved (the
// channel masters share one writer, hence one row order — no per-file reconciliation needed).
const (
	limbLitFracMin = 0.005 // minimum lit fraction of the frame for the field to be meaningful
	limbBlurDiv    = 24    // blur radius = min(W,H)/limbBlurDiv; three box passes ≈ gaussian
	limbTopP       = 99.9  // illumination normalization percentile (the "bright limb" level)
)

// limbShoulder maps normalized illumination through a tanh shoulder: exact identity below knee,
// compressing [knee..] toward ceil — the deep-sky core-shoulder curve, applied to the illumination
// BASE rather than to pixel values, so detail riding on the base is never flattened.
func limbShoulder(x, knee, ceil float64) float64 {
	if x <= knee {
		return x
	}
	span := ceil - knee
	return knee + span*math.Tanh((x-knee)/span)
}

// limbBalance writes illumination-compressed copies of the channel masters (limb_ prefix, beside
// the originals — the persisted masters stay pristine so a supervised re-finish never compounds the
// compression) and returns the swapped bases plus a user-facing note. Zero strength, no luminance
// source or no lit surface → inputs returned untouched (soft no-op).
func limbBalance(r, g, b, l, mono string, strength float64) (nr, ng, nb, nl, nmono, note string) {
	nr, ng, nb, nl, nmono = r, g, b, l, mono
	if strength <= 0 {
		return
	}
	if strength > 1 {
		strength = 1
	}
	lumBase := firstBase(l, mono, g, r, b)
	if lumBase == "" {
		return
	}
	lum, err := fits.ReadImage(lumBase + ".fits")
	if err != nil || len(lum.Pix) == 0 {
		return
	}
	gain, litFrac := limbGainField(lum.Pix[0], lum.W, lum.H, strength)
	if gain == nil {
		return
	}
	apply := func(base string) string {
		if base == "" {
			return ""
		}
		im, err := fits.ReadImage(base + ".fits")
		if err != nil || len(im.Pix) == 0 || im.W != lum.W || im.H != lum.H {
			return base // unreadable or off-grid — keep the original channel untouched
		}
		for _, p := range im.Pix {
			for i, v := range p {
				p[i] = v * gain[i]
			}
		}
		out := filepath.Join(filepath.Dir(base), "limb_"+filepath.Base(base))
		if err := im.WriteFITS(out + ".fits"); err != nil {
			return base
		}
		return out
	}
	nr, ng, nb, nl, nmono = apply(r), apply(g), apply(b), apply(l), apply(mono)
	note = fmt.Sprintf("limb balance: illumination compressed above %d%% of the bright-limb level (strength %.2f, lit %.0f%% of frame)",
		int((1-0.6*strength)*100), strength, litFrac*100)
	return
}

// limbGainField builds the per-pixel gain that compresses the smooth illumination base: exactly 1
// below the knee (sky, terminator, mid-tones) and <1 across the bright limb band. nil when the
// frame carries no meaningful lit surface.
func limbGainField(p []float32, w, h int, strength float64) ([]float32, float64) {
	top := imgops.Percentile(imgops.Subsample(p, 200_000), 99.5)
	if !(top > 0) {
		return nil, 0
	}
	thr := float32(0.05 * top)
	vals := make([]float32, len(p)) // v·mask
	wts := make([]float32, len(p))  // mask
	lit := 0
	for i, v := range p {
		if v > thr {
			vals[i], wts[i] = v, 1
			lit++
		}
	}
	litFrac := float64(lit) / float64(len(p))
	if litFrac < limbLitFracMin {
		return nil, litFrac
	}
	// Normalized convolution restricted to the lit surface: the black sky must not drag the
	// illumination down near the limb (that would push the gain the WRONG way at the very edge).
	rad := min(w, h) / limbBlurDiv
	if rad < 4 {
		rad = 4
	}
	boxBlur3(vals, w, h, rad)
	boxBlur3(wts, w, h, rad)
	illum := vals
	for i := range illum {
		if wts[i] > 1e-6 {
			illum[i] /= wts[i]
		} else {
			illum[i] = 0
		}
	}
	litIllum := make([]float32, 0, 200_000)
	stride := len(p)/200_000 + 1
	for i := 0; i < len(p); i += stride {
		if p[i] > thr {
			litIllum = append(litIllum, illum[i])
		}
	}
	m := imgops.Percentile(litIllum, limbTopP)
	if !(m > 0) {
		return nil, litFrac
	}
	knee := 1 - 0.6*strength
	ceil := 1 - 0.22*strength
	gain := make([]float32, len(p))
	for i := range gain {
		x := float64(illum[i]) / m
		if x <= knee {
			gain[i] = 1
			continue
		}
		gain[i] = float32(limbShoulder(x, knee, ceil) / x)
	}
	return gain, litFrac
}

// boxBlur3 runs three separable box-blur passes of the given radius in place (≈ a gaussian of
// σ ≈ rad — smooth enough that crater-scale detail never enters the illumination estimate).
func boxBlur3(p []float32, w, h, rad int) {
	tmp := make([]float32, len(p))
	for pass := 0; pass < 3; pass++ {
		boxBlurH(p, tmp, w, h, rad)
		boxBlurH(tmp, p, h, w, rad) // transposed second axis: tmp is column-major of p
	}
}

// boxBlurH box-blurs rows of src (w×h, row-major) into dst TRANSPOSED (dst is h×w) — two calls blur
// both axes and land back in the original orientation.
func boxBlurH(src, dst []float32, w, h, rad int) {
	for y := 0; y < h; y++ {
		row := src[y*w : (y+1)*w]
		var sum float64
		n := 0
		for x := 0; x < min(rad+1, w); x++ {
			sum += float64(row[x])
			n++
		}
		for x := 0; x < w; x++ {
			dst[x*h+y] = float32(sum / float64(n))
			if nx := x + rad + 1; nx < w {
				sum += float64(row[nx])
				n++
			}
			if px := x - rad; px >= 0 {
				sum -= float64(row[px])
				n--
			}
		}
	}
}
