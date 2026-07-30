package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Earthshine colour. v1 tinted the whole dark side with ONE global triple (and fell back to
// plain neutral when a master was missing) — against the lit disc's own rendered tone that
// read as a colour step at the terminator. v2 renders the dark side as the SAME moon: a
// per-pixel chromaticity from the linear R/G/B masters (the applyTrueLum ε/ratio pattern),
// smoothed (dark-side chroma is noise-dominated), desaturated toward the LIT side's own
// median tone, renormalized per pixel to mean 1 (the layer carries the luminance, the
// chroma only the colour) and clamped. Any missing master degrades to the lit tone itself —
// strictly better than neutral.

// litChroma measures the finish's rendered lit-side colour: per-channel medians over solid
// lit pixels (master above the lit threshold, deep inside the disc alpha), normalized to
// mean 1. Mono finishes and degenerate discs return neutral.
func litChroma(finish *fits.Image, g *discGeom, detail *fits.Image) [3]float64 {
	neutral := [3]float64{1, 1, 1}
	if finish.C != 3 {
		return neutral
	}
	p := detail.Pix[0]
	bg, pk, ok := litStats(p)
	if !ok {
		return neutral
	}
	thr := litThreshold(bg, pk)
	stride := len(g.alpha)/200000 + 1
	var vals [3][]float64
	for li := 0; li < len(g.alpha); li += stride {
		if g.alpha[li] <= 0.9 || p[g.full(li)] <= thr {
			continue
		}
		fi := g.full(li)
		for c := 0; c < 3; c++ {
			vals[c] = append(vals[c], float64(finish.Pix[c][fi]))
		}
	}
	if len(vals[0]) < 500 {
		return neutral
	}
	var med [3]float64
	mean := 0.0
	for c := 0; c < 3; c++ {
		med[c] = medianOf(vals[c])
		mean += med[c] / 3
	}
	if mean <= 1e-6 {
		return neutral
	}
	var tint [3]float64
	for c := 0; c < 3; c++ {
		tint[c] = clampF(med[c]/mean, esChromaLo, esChromaHi)
	}
	return tint
}

// chromaPlanes builds the earthshine layer's per-pixel colour from the linear R/G/B masters,
// blended toward the lit side's tone (esChromaKeep of the dark side's own colour survives).
// Returns nil for mono finishes; any unreadable/mismatched master falls back to the flat lit
// tone (flatChroma) with mode "lit-fallback".
func chromaPlanes(finish *fits.Image, g *discGeom, order, rBase, gBase, bBase string,
	litTint [3]float64, darkMed float64) (planes [][]float32, mode string) {
	if finish.C != 3 {
		return nil, "mono"
	}
	var masters [3]*fits.Image
	for i, base := range []string{rBase, gBase, bBase} {
		m, err := readAligned(base, finish, order)
		if err != nil {
			return flatChroma(litTint, g), "lit-fallback"
		}
		masters[i] = m
	}
	rel := relChromaPlanes(masters, g, darkMed)
	planes = make([][]float32, 3)
	for c := range planes {
		planes[c] = make([]float32, g.bw*g.bh)
	}
	for li := 0; li < g.bw*g.bh; li++ {
		var t [3]float64
		sum := 0.0
		for c := 0; c < 3; c++ {
			t[c] = (1-esChromaKeep)*litTint[c] + esChromaKeep*float64(rel[c][li])
			sum += t[c]
		}
		for c := 0; c < 3; c++ {
			planes[c][li] = float32(clampF(t[c]*3/math.Max(sum, 1e-9), esChromaLo, esChromaHi))
		}
	}
	return planes, "perpixel"
}

// relChromaPlanes is each channel's smoothed linear dark-side ratio to the per-pixel channel
// mean (ε-stabilized so black pixels read neutral, the applyTrueLum pattern).
func relChromaPlanes(masters [3]*fits.Image, g *discGeom, darkMed float64) [3][]float32 {
	eps := 0.05 * darkMed
	var sky [3]float64
	for c, m := range masters {
		sky[c], _, _ = skyStats(m.Pix[0], g)
	}
	var rel [3][]float32
	for c := range rel {
		rel[c] = make([]float32, g.bw*g.bh)
	}
	for li := 0; li < g.bw*g.bh; li++ {
		fi := g.full(li)
		var sig [3]float64
		mean := 0.0
		for c := 0; c < 3; c++ {
			sig[c] = math.Max(0, float64(masters[c].Pix[0][fi])-sky[c])
			mean += sig[c] / 3
		}
		for c := 0; c < 3; c++ {
			rel[c][li] = float32(clampF((sig[c]+eps)/(mean+eps), 0.25, 4))
		}
	}
	sig := clampF(g.fit.R/esChromaSigDiv, esChromaSigMin, esChromaSigMax)
	for c := 0; c < 3; c++ {
		rel[c] = imgops.GaussianBlur(rel[c], g.bw, g.bh, sig)
	}
	return rel
}

// flatChroma fills the trio with the lit side's own tone — the colour-match fallback when a
// linear colour master is unavailable.
func flatChroma(litTint [3]float64, g *discGeom) [][]float32 {
	planes := make([][]float32, 3)
	for c := range planes {
		planes[c] = make([]float32, g.bw*g.bh)
		for li := range planes[c] {
			planes[c][li] = float32(litTint[c])
		}
	}
	return planes
}
