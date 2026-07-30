package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// Earthshine geometry + confinement: the fitted-disc alpha, the lit-surface threshold family,
// the enclave guard, and the illumination mask that decides — per pixel — how much of the
// toned earthshine layer may show. The mask is the v2 confinement: exactly 0 over the
// (dilated) lit surface, ramping to 1 in master-VALUE space between the lit threshold and the
// glow level where the tone shoulder saturates. Driving the ramp in value space (not in
// blurred-distance space) co-locates the handover with the finish's own terminator ramp no
// matter how steep the terminator is — a geometric σ∝R ramp dips visibly on steep ones (the
// historical "moat").

// discGeom is the fitted disc rasterized over the finish frame: a bbox crop (disc + feather)
// with a feathered full-disc alpha, plus local↔full index mapping.
type discGeom struct {
	w, h           int // full frame
	x0, y0, bw, bh int // bbox
	fit            discFit
	feather        float64
	alpha          []float32 // bw*bh, 1 inside the disc → 0 past the limb feather
}

func (g *discGeom) full(li int) int { return (g.y0+li/g.bw)*g.w + g.x0 + li%g.bw }

// newDiscGeom rasterizes the fitted disc into a feathered alpha over its bounding box.
func newDiscGeom(w, h int, fit discFit) *discGeom {
	feather := math.Min(math.Max(0.01*fit.R, esFeatherMin), esFeatherMax)
	pad := fit.R + feather + 2
	g := &discGeom{
		w: w, h: h, fit: fit, feather: feather,
		x0: clampInt(int(fit.CX-pad), 0, w-1), y0: clampInt(int(fit.CY-pad), 0, h-1),
	}
	x1 := clampInt(int(fit.CX+pad)+1, 1, w)
	y1 := clampInt(int(fit.CY+pad)+1, 1, h)
	g.bw, g.bh = x1-g.x0, y1-g.y0
	g.alpha = make([]float32, g.bw*g.bh)
	for ly := 0; ly < g.bh; ly++ {
		for lx := 0; lx < g.bw; lx++ {
			d := math.Hypot(float64(g.x0+lx)-fit.CX, float64(g.y0+ly)-fit.CY)
			g.alpha[ly*g.bw+lx] = float32(smoothstep((fit.R + feather - d) / (2 * feather)))
		}
	}
	return g
}

func smoothstep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

// litStats returns a plane's robust background/peak — the base of the shared lit-threshold
// family (disc fit, AP disk mask, earthshine mask all derive from it).
func litStats(p []float32) (bg, pk float64, ok bool) {
	bg = lowPercentile(p, 0.2)
	pk = lowPercentile(p, 0.999)
	return bg, pk, pk-bg > 1e-9
}

// litThreshold is the shared lit-surface threshold: bg + apDiskFrac·(pk − bg).
func litThreshold(bg, pk float64) float32 {
	return float32(bg + apDiskFrac*(pk-bg))
}

// strongDarkSet returns the bbox-local indices confidently on the unlit disc: deep inside the
// feathered alpha and well below the lit threshold in the linear master (esDarkFrac of the same
// threshold ramp the disc fit used), so terminator pixels never pollute the dark-side statistics.
func strongDarkSet(detail *fits.Image, g *discGeom) []int {
	p := detail.Pix[0]
	bg, pk, ok := litStats(p)
	if !ok {
		return nil
	}
	thr := float32(bg + esDarkFrac*apDiskFrac*(pk-bg))
	var set []int
	for li, a := range g.alpha {
		if a > 0.9 && p[g.full(li)] <= thr {
			set = append(set, li)
		}
	}
	return set
}

// litGuard protects small master-dark ENCLAVES deep inside the lit zone (crater shadows): the
// lit mask is blurred and the guard fades to 0 over blurred values [esGuardLo, esGuardHi] —
// enclaves up to ~0.64σ diameter stay untouched while larger shadows/bays connected to the
// night side receive the physically-correct earthshine fill. Its historical second job —
// fading the lift at the terminator — moved to the illumination mask, which zeroes the lit
// surface exactly; the guard never binds there anymore.
func litGuard(detail *fits.Image, g *discGeom) []float32 {
	p := detail.Pix[0]
	bg, pk, ok := litStats(p)
	if !ok {
		return nil
	}
	thr := litThreshold(bg, pk)
	mask := make([]float32, g.bw*g.bh)
	for li := range mask {
		if p[g.full(li)] > thr {
			mask[li] = 1
		}
	}
	soft := imgops.GaussianBlur(mask, g.bw, g.bh, guardSigma(g.fit.R))
	for li, s := range soft {
		soft[li] = float32(1 - smoothstep((float64(s)-esGuardLo)/(esGuardHi-esGuardLo)))
	}
	return soft
}

// guardSigma is the lit-guard blur radius for a disc of radius r (px).
func guardSigma(r float64) float64 {
	return math.Min(math.Max(esGuardSigFrac*r, esGuardSigMin), esGuardSigMax)
}

// illuminationMask assembles the earthshine confinement — m = valueRamp × litGuard × alpha,
// then EXACTLY 0 wherever the dilated lit mask is set (applied last, unconditionally): the
// composite skips m ≤ 0 before any float op, so the lit surface stays byte-identical in the
// strongest sense. The value ramp runs from the lit threshold (m→0) down to the glow level
// where the tone shoulder saturates (m→1); pGlow above 0.9·thr degenerates to 0.9·thr so the
// ramp can never invert. Returns the glow level actually used, for provenance.
func illuminationMask(detail *fits.Image, g *discGeom, guard []float32, pGlow, sigAA float64, dilate int) ([]float32, float64) {
	p := detail.Pix[0]
	bg, pk, ok := litStats(p)
	if !ok {
		return nil, 0
	}
	thr := float64(litThreshold(bg, pk))
	glow := math.Min(pGlow, 0.9*thr)
	crop := make([]float32, g.bw*g.bh)
	lit := make([]bool, g.bw*g.bh)
	for li := range crop {
		crop[li] = p[g.full(li)]
		lit[li] = float64(crop[li]) > thr
	}
	hard := imgops.BinaryDilation(lit, g.bw, g.bh, dilate)
	pAA := imgops.GaussianBlur(crop, g.bw, g.bh, sigAA)
	m := make([]float32, g.bw*g.bh)
	for li := range m {
		if hard[li] {
			continue // exactly 0: no arithmetic ever touches the lit surface
		}
		u := (thr - float64(pAA[li])) / (thr - glow)
		m[li] = float32(smoothstep(clampF(u, 0, 1))) * g.alpha[li] * guard[li]
	}
	return m, glow
}
