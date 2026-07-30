package planetary

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// Earthshine statistics + tone. The v2 tone map keeps the proven asinh anchoring (dark-side
// median → level, so the gain semantics are exact) but replaces the flat relative cap with a
// C¹ tanh shoulder toward an ADAPTIVE ceiling derived from the dark side's own P99.5: bright
// earthshine features (Aristarchus-class rays) keep headroom and relief instead of clipping
// into the flat grey band the cap produced.

// esShoulder is one render's derived tone-shoulder geometry.
type esShoulder struct {
	level float64 // dark-side median display level (esTargetLevel × gain)
	knee  float64 // where the shoulder takes over from the asinh base
	ceil  float64 // asymptotic ceiling (adaptive, clamped, ≤ esCapAbs)
	pGlow float64 // master value whose BASE tone reaches ceil — the mask ramp's dark end
	xP    float64 // the dark-side P99.5 of sig/darkMed that sized the ceiling (provenance)
}

// shoulderParams derives the tone shoulder from the dark side's own statistics. The ceiling
// tracks 1.15× the base tone of the P99.5 signal — clamped to [1.3, 2.6]×level and the
// absolute safety esCapAbs — so the shoulder compresses only the extreme tail. pGlow ties the
// illumination mask to the tone: the mask saturates exactly where the tone stops changing,
// making the terminator handover structurally continuous (no measured feedback into the tone).
func shoulderParams(detail *fits.Image, g *discGeom, dark []int, skyMed, darkMed, gain float64) esShoulder {
	level := esTargetLevel * gain
	p := detail.Pix[0]
	stride := len(dark)/100000 + 1
	xs := make([]float64, 0, len(dark)/stride+1)
	for i := 0; i < len(dark); i += stride {
		xs = append(xs, math.Max(0, float64(p[g.full(dark[i])])-skyMed)/darkMed)
	}
	sort.Float64s(xs)
	xP := xs[int(esShoulderP*float64(len(xs)-1))]
	base := level * math.Asinh(esAsinhK*xP) / math.Asinh(esAsinhK)
	ceil := math.Min(clampF(esKneeK*base, esShoulderLoK*level, esShoulderHiK*level), esCapAbs)
	pGlow := skyMed + (darkMed/esAsinhK)*math.Sinh(math.Asinh(esAsinhK)*ceil/level)
	return esShoulder{level: level, knee: esKneeK * level, ceil: ceil, pGlow: pGlow, xP: xP}
}

// earthshineLayer tones the linear dark-side signal into a display-space level map: sky-
// subtracted, asinh-anchored so the dark median lands exactly at level, rolling into the tanh
// shoulder above the knee — monotone, C¹ (tanh′(0)=1), never flat, never above ceil — then
// starlet-denoised at moderate strength (SNR-protected) so lifted read-noise doesn't grain the
// shadow without waxing the relief away. The mask is deliberately NOT baked in here — the
// denoise would bleed it.
func earthshineLayer(detail *fits.Image, g *discGeom, skyMed, darkMed float64, sh esShoulder) []float32 {
	p := detail.Pix[0]
	v := make([]float32, g.bw*g.bh)
	norm := math.Asinh(esAsinhK)
	for li := range v {
		sig := math.Max(0, float64(p[g.full(li)])-skyMed)
		tone := sh.level * math.Asinh(esAsinhK*sig/darkMed) / norm
		if tone > sh.knee {
			tone = sh.knee + (sh.ceil-sh.knee)*math.Tanh((tone-sh.knee)/(sh.ceil-sh.knee))
		}
		v[li] = float32(tone)
	}
	crop := &fits.Image{W: g.bw, H: g.bh, C: 1, Pix: [][]float32{v}}
	noise.Denoise(crop, noise.Options{Strength: esDenoiseStrength})
	return crop.Pix[0]
}

// glowRingP25 measures the finish luminance where the master sits just above the glow level
// (the mask ramp's foot) — pure telemetry for the terminator handover. It is never fed back
// into the tone: a feedback coupling crushes legitimate deep-dark relief whenever a
// hard-stretched finish renders the glow foot near black.
func glowRingP25(detail *fits.Image, g *discGeom, finLum []float32, glow float64) float64 {
	p := detail.Pix[0]
	vals := make([]float64, 0, 4096)
	for li, a := range g.alpha {
		if a <= 0.9 {
			continue
		}
		v := float64(p[g.full(li)])
		if v >= glow && v <= 1.5*glow {
			vals = append(vals, float64(finLum[li]))
		}
	}
	if len(vals) < 100 {
		return 0
	}
	sort.Float64s(vals)
	return vals[len(vals)/4]
}

// skyStats samples the plane outside the disc (r + margin) for a robust background level. When the
// disc fills the frame it falls back to the darkest 2% of the plane, with a note.
func skyStats(p []float32, g *discGeom) (med, mad float64, note string) {
	vals := make([]float64, 0, 100000)
	stride := len(p)/200000 + 1
	limit := g.fit.R + esSkyMargin
	for i := 0; i < len(p); i += stride {
		if math.Hypot(float64(i%g.w)-g.fit.CX, float64(i/g.w)-g.fit.CY) > limit {
			vals = append(vals, float64(p[i]))
		}
	}
	if len(vals) >= 500 {
		return medianMAD(vals)
	}
	med = lowPercentile(p, 0.02)
	return med, math.Max(med*0.5, 1e-7), "earthshine: disc fills the frame — sky level estimated from the darkest 2%"
}

func medianMAD(vals []float64) (med, mad float64, note string) {
	med = medianOf(append([]float64(nil), vals...))
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - med)
	}
	return med, medianOf(dev), ""
}

func medianOverSet(p []float32, g *discGeom, set []int) float64 {
	vals := make([]float64, len(set))
	for i, li := range set {
		vals[i] = float64(p[g.full(li)])
	}
	return medianOf(vals)
}

func medianOverLocal(crop []float32, set []int) float64 {
	vals := make([]float64, len(set))
	for i, li := range set {
		vals[i] = float64(crop[li])
	}
	return medianOf(vals)
}
