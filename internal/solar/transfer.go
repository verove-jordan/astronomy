package solar

import (
	"math"
	"strings"
)

// transfer.go linearises the pixels an iPhone clip hands us. Every clip in a real solar session
// recorded on an iPhone 16 Pro comes back as 10-bit HLG (`color_transfer=arib-std-b67`), and the
// host ffmpeg is built without zscale, so `zscale=t=linear` is unavailable — the inversion has to
// happen here. It is not cosmetic: stacking, photometric normalisation and Richardson-Lucy all
// assume linear light, and a tone-mapped frame breaks all three.

type transferFn int

const (
	transferSDR transferFn = iota // bt709/unknown: near-linear enough after the sRGB-ish gamma below
	transferHLG                   // ITU-R BT.2100 hybrid log-gamma
	transferPQ                    // SMPTE ST 2084 perceptual quantiser
)

// transferKind maps an ffprobe color_transfer string onto the inversion we must apply.
func transferKind(s string) transferFn {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "arib-std-b67", "hlg":
		return transferHLG
	case "smpte2084", "pq":
		return transferPQ
	default:
		return transferSDR
	}
}

// HLG inverse-OETF constants (ITU-R BT.2100 Table 5): b = 1−4a, c = 0.5 − a·ln(4a).
const (
	hlgA = 0.17883277
	hlgB = 0.28466892
	hlgC = 0.55991073
)

// PQ inverse-EOTF constants (SMPTE ST 2084).
const (
	pqM1 = 2610.0 / 16384.0
	pqM2 = 2523.0 / 4096.0 * 128.0
	pqC1 = 3424.0 / 4096.0
	pqC2 = 2413.0 / 4096.0 * 32.0
	pqC3 = 2392.0 / 4096.0 * 32.0
)

// linearizeHLG inverts the HLG opto-electronic transfer function in place, mapping the signal back
// to SCENE-referred linear light.
//
// Note it is the OETF, not the EOTF, that is inverted here, and no OOTF is applied: BT.2100 defines
// HLG's OETF over scene light, with the system OOTF living in the display. So inverting the OETF
// alone already lands us in relative scene linear — exactly the space photometry wants.
func linearizeHLG(p []float32) {
	for i, v := range p {
		e := float64(v)
		switch {
		case e <= 0:
			e = 0
		case e <= 0.5:
			e = e * e / 3
		default:
			e = (math.Exp((e-hlgC)/hlgA) + hlgB) / 12
		}
		p[i] = float32(e)
	}
}

// linearizePQ inverts the PQ transfer function in place. PQ is display-referred absolute luminance;
// the result is normalised to its 10 000 cd/m² peak, which is monotone in light and therefore fine
// for everything downstream (we only ever use relative intensities).
func linearizePQ(p []float32) {
	for i, v := range p {
		e := float64(v)
		if e <= 0 {
			p[i] = 0
			continue
		}
		em := math.Pow(e, 1/pqM2)
		num := math.Max(em-pqC1, 0)
		den := pqC2 - pqC3*em
		if den <= 0 {
			p[i] = 0
			continue
		}
		p[i] = float32(math.Pow(num/den, 1/pqM1))
	}
}

// linearizeSDRGamma inverts the BT.709/sRGB-ish display gamma of a non-HDR clip. Cameras encode
// roughly 1/2.2; we use the exact sRGB piecewise curve because it is what `sips` and dcraw's
// `-g 2.4 12.92` also produce, so a still and a video of the same Sun land in the same space.
func linearizeSDRGamma(p []float32) {
	for i, v := range p {
		e := float64(v)
		switch {
		case e <= 0:
			e = 0
		case e <= 0.04045:
			e /= 12.92
		default:
			e = math.Pow((e+0.055)/1.055, 2.4)
		}
		p[i] = float32(e)
	}
}

// limitedRangeBounds returns the normalised black and white levels of a limited-range ("tv") luma
// plane at the given bit depth. ffmpeg widens a 10-bit sample to 16 bits by bit replication, so the
// 10-bit 64..940 window lands at 64/1023..940/1023 once divided by the 16-bit full scale — hence
// the depth-specific fractions rather than the 8-bit 16/255..235/255 everyone quotes.
func limitedRangeBounds(bitDepth int) (lo, hi float64) {
	if bitDepth < 8 {
		bitDepth = 8
	}
	maxVal := float64(int(1)<<bitDepth - 1)
	shift := float64(int(1) << (bitDepth - 8))
	return 16 * shift / maxVal, 235 * shift / maxVal
}

// expandRange maps a limited-range luma plane onto 0..1 in place. A full-range plane is left alone.
// Values outside the window are kept (not clamped at the top) so a genuinely clipped disc still
// reads as clipped downstream instead of being quietly folded to exactly 1.0.
func expandRange(p []float32, bitDepth int, fullRange bool) {
	if fullRange {
		return
	}
	lo, hi := limitedRangeBounds(bitDepth)
	span := hi - lo
	if span <= 0 {
		return
	}
	for i, v := range p {
		e := (float64(v) - lo) / span
		if e < 0 {
			e = 0
		}
		p[i] = float32(e)
	}
}

// Linearize takes a decoded luma plane in signal space and returns it in linear light: expand the
// limited range first, then invert whichever transfer function the container declared.
func Linearize(p []float32, info VideoInfo) {
	expandRange(p, info.BitDepth, info.FullRange())
	switch transferKind(info.Transfer) {
	case transferHLG:
		linearizeHLG(p)
	case transferPQ:
		linearizePQ(p)
	default:
		linearizeSDRGamma(p)
	}
}
