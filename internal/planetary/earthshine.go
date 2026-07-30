package planetary

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Earthshine reveal (v2): lift the Moon's UNLIT side into visibility on the finished render,
// from the real stacked signal in the linear masters — never synthesized. Geometry/masking
// lives in esmask.go, tone in estone.go, colour in escolor.go; this file orchestrates and
// composites: out_c = finish_c + m·max(0, E·t_c − finish_c), per channel. The illumination
// mask m is EXACTLY 0 over the (dilated) lit surface — no arithmetic ever touches a lit pixel
// — and ramps to 1 in master-value space precisely as fast as the tone shoulder saturates, so
// the terminator handover is a smooth per-channel lighten with no cap contour, no moat and no
// colour step (the dark side is chroma-matched to the lit disc's own rendered tone). Every
// failure path is a skip note — the run never fails and the finish FITS is left untouched.
const (
	esTargetLevel = 0.10 // display level the dark-side median lands at, × gain
	esFeatherMin  = 2.0  // limb feather of the disc alpha (px): clamp(0.01·r, min, max)
	esFeatherMax  = 8.0
	esDarkFrac    = 0.5   // "confidently unlit" = below esDarkFrac × the lit threshold ramp
	esSkyMargin   = 12.0  // sky annulus starts at r + margin (px)
	esSNRK        = 0.5   // dark-side median must exceed esSNRK·1.4826·skyMAD above the sky
	esMinDarkPx   = 2000  // fewer strong-dark pixels = full moon / sliver → nothing to lift
	esMinLift     = 0.005 // finish already at target → nothing to do
	esAsinhK      = 3.0   // asinh anchor shape of the dark layer

	// Tone shoulder (estone.go): the ceiling adapts to the dark side's P99.5 so the brightest
	// earthshine features (Aristarchus-class) keep relief instead of clipping into a flat band.
	esKneeK           = 1.15  // knee (× level) where the tanh shoulder takes over; also × base(P99.5) for the ceiling
	esShoulderLoK     = 1.3   // adaptive ceiling clamp, × level
	esShoulderHiK     = 2.6   // (1.3 < always > esKneeK, so the shoulder never degenerates)
	esShoulderP       = 0.995 // dark-side percentile anchoring the ceiling
	esCapAbs          = 0.35  // absolute display safety ceiling — never binds at gain ≤ 2
	esDenoiseStrength = 0.6   // starlet denoise of the layer (v1's full strength waxed the relief away)

	// Illumination mask (esmask.go): feather is a FRACTION of the disc radius (drizzle-safe)
	// and drives ONLY the hard lit-mask dilation — the byte-identical protection margin. The
	// value-ramp lookup keeps a small FIXED anti-noise blur: a large lookup blur mixes lit
	// values into near-terminator dark pixels and fades the mask early (a moat).
	esFeatherFracDefault = 0.006
	esFeatherFracMin     = 0.002
	esFeatherFracMax     = 0.02
	esMaskLookupSigma    = 1.5
	esDilateMax          = 6

	// litGuard (esmask.go) — v2 role: ENCLAVE protection only (crater shadows deep inside the
	// lit zone stay untouched up to ~0.64σ diameter; bays connected to the night side fill).
	// The terminator confinement moved to the illumination mask.
	esGuardLo      = 0.5
	esGuardHi      = 0.95
	esGuardSigFrac = 0.02
	esGuardSigMin  = 4.0
	esGuardSigMax  = 12.0

	// Colour (escolor.go): per-pixel dark-side chroma, smoothed and desaturated toward the lit
	// disc's own rendered tone.
	esChromaKeep   = 0.3 // keep 30% of the dark side's own colour
	esChromaSigDiv = 64.0
	esChromaSigMin = 3.0
	esChromaSigMax = 12.0
	esChromaLo     = 0.7
	esChromaHi     = 1.4
)

// applyEarthshine composites the earthshine layer into outBase.fits (the finished, stretched image)
// using the linear channel masters. Returns human-readable notes; never an error (soft-fail).
func applyEarthshine(rBase, gBase, bBase, lBase, monoBase, outBase string, fin siril.PlanetaryFinish) []string {
	gain := fin.EarthshineGain
	if gain <= 0 {
		return nil
	}
	finish, order, err := readFinish(outBase)
	if err != nil {
		return []string{"earthshine skipped: " + err.Error()}
	}
	detail, err := readAligned(firstBase(lBase, monoBase, gBase, rBase, bBase), finish, order)
	if err != nil {
		return []string{"earthshine skipped: " + err.Error()}
	}
	fit, ok := fitLunarDisc(detail)
	if !ok {
		return noteAndRecord(outBase, esRecord{Gain: gain, Reason: "lunar disc not found (circle fit failed)"})
	}
	g := newDiscGeom(finish.W, finish.H, fit)
	feather := clampF(featherOrDefault(fin.EarthshineFeather), esFeatherFracMin, esFeatherFracMax)
	rec := esRecord{Gain: gain, Feather: feather, CX: fit.CX, CY: fit.CY, R: fit.R, Inliers: fit.Inliers, ArcDeg: fit.ArcDeg}
	dark := strongDarkSet(detail, g)
	if len(dark) < esMinDarkPx {
		rec.Reason = "disc nearly fully lit — nothing to lift"
		return noteAndRecord(outBase, rec)
	}
	skyMed, skyMAD, skyNote := skyStats(detail.Pix[0], g)
	darkMed := medianOverSet(detail.Pix[0], g, dark) - skyMed
	rec.SkyLevel, rec.DarkMedian = skyMed, darkMed
	if darkMed <= esSNRK*1.4826*math.Max(skyMAD, 1e-7) {
		rec.Reason = "dark-side signal below the noise floor"
		return noteAndRecord(outBase, rec)
	}
	finLum := finishLuminance(finish, g)
	sh := shoulderParams(detail, g, dark, skyMed, math.Max(darkMed, 1e-7), gain)
	rec.fillShoulder(sh, finLum, dark)
	if sh.level-rec.FinDarkMedian <= esMinLift {
		rec.Reason = "dark side already at target level"
		return noteAndRecord(outBase, rec)
	}
	notes := renderEarthshine(finish, detail, g, order, rBase, gBase, bBase, outBase,
		skyMed, math.Max(darkMed, 1e-7), feather, sh, finLum, &rec)
	if skyNote != "" {
		notes = append(notes, skyNote)
	}
	return notes
}

// renderEarthshine builds the layer, mask and chroma, composites and writes — the render half
// of applyEarthshine (the gates/statistics half stays in the caller).
func renderEarthshine(finish, detail *fits.Image, g *discGeom, order, rBase, gBase, bBase, outBase string,
	skyMed, darkMed, feather float64, sh esShoulder, finLum []float32, rec *esRecord) []string {
	layer := earthshineLayer(detail, g, skyMed, darkMed, sh)
	guard := litGuard(detail, g)
	if guard == nil {
		rec.Reason = "degenerate master range"
		return noteAndRecord(outBase, *rec)
	}
	rec.GuardSigma = guardSigma(g.fit.R)
	dilate := clampInt(int(math.Round(feather*g.fit.R/2)), 1, esDilateMax)
	rec.FeatherSigma, rec.DilatePx = esMaskLookupSigma, dilate
	mask, glow := illuminationMask(detail, g, guard, sh.pGlow, esMaskLookupSigma, dilate)
	if mask == nil {
		rec.Reason = "degenerate master range"
		return noteAndRecord(outBase, *rec)
	}
	rec.GlowLevel, rec.GlowRingP25 = glow, glowRingP25(detail, g, finLum, glow)
	litTint := litChroma(finish, g, detail)
	chroma, mode := chromaPlanes(finish, g, order, rBase, gBase, bBase, litTint, darkMed)
	rec.ChromaMode = mode
	rec.LitChroma = append([]float64(nil), litTint[:]...)
	compositeDarkLayer(finish, g, mask, layer, chroma)
	if werr := finish.OverwriteData(outBase + ".fits"); werr != nil {
		return []string{"earthshine skipped: write composite: " + werr.Error()}
	}
	rec.Applied = true
	notes := noteAndRecord(outBase, *rec)
	if rec.GlowRingP25 > 0 && sh.ceil > 3*rec.GlowRingP25 {
		notes = append(notes, fmt.Sprintf(
			"earthshine: shoulder ceiling %.2f is >3x the finish's glow ring (%.2f) — the terminator may read bright at this gain",
			sh.ceil, rec.GlowRingP25))
	}
	return notes
}

// featherOrDefault resolves the feather knob: 0 (unset) means the default fraction.
func featherOrDefault(f float64) float64 {
	if f <= 0 {
		return esFeatherFracDefault
	}
	return f
}

// compositeDarkLayer blends the toned layer over the finish as a per-channel EXCESS lift under
// the illumination mask: out_c = finish_c + m·max(0, E·t_c − finish_c). Outside the mask's
// support (sky, dilated lit surface, protected enclaves) NOTHING is touched — the skip runs
// before any float op, so those bytes are identical by construction. Where the finish already
// outshines the layer the excess is zero; the mask ramp and the tone shoulder saturate
// together (pGlow), so the handover is value- and slope-continuous.
func compositeDarkLayer(finish *fits.Image, g *discGeom, mask, layer []float32, chroma [][]float32) {
	for li, m := range mask {
		if m <= 0 {
			continue
		}
		fi := g.full(li)
		for c := 0; c < finish.C; c++ {
			e := float64(layer[li])
			if chroma != nil {
				e *= float64(chroma[c][li])
			}
			excess := e - float64(finish.Pix[c][fi])
			if excess <= 0 {
				continue
			}
			finish.Pix[c][fi] = float32(math.Min(1, float64(finish.Pix[c][fi])+float64(m)*excess))
		}
	}
}

// readFinish loads the finished image and its row order.
func readFinish(outBase string) (*fits.Image, string, error) {
	path := outBase + ".fits"
	im, err := fits.ReadImage(path)
	if err != nil {
		return nil, "", fmt.Errorf("read finish %s: %w", filepath.Base(path), err)
	}
	return im, rowOrder(path), nil
}

// rowOrder reads a file's ROWORDER card; a missing card means the historical bottom-up convention.
func rowOrder(path string) string {
	f, err := fits.Open(path)
	if err != nil {
		return "BOTTOM-UP"
	}
	if order, ok := f.Header.String("ROWORDER"); ok && order != "" {
		return order
	}
	return "BOTTOM-UP"
}

// readAligned loads a master and reconciles it onto the finish's grid: dimensions must match and a
// differing ROWORDER (Go-written masters are TOP-DOWN, Siril output may not be) is flipped in memory.
func readAligned(base string, ref *fits.Image, refOrder string) (*fits.Image, error) {
	if base == "" {
		return nil, fmt.Errorf("no master available")
	}
	path := base + ".fits"
	im, err := fits.ReadImage(path)
	if err != nil {
		return nil, fmt.Errorf("read master %s: %w", filepath.Base(path), err)
	}
	if im.W != ref.W || im.H != ref.H {
		return nil, fmt.Errorf("master %s is %dx%d, finish is %dx%d", filepath.Base(path), im.W, im.H, ref.W, ref.H)
	}
	if rowOrder(path) != refOrder {
		flipRows(im)
	}
	return im, nil
}

// flipRows mirrors every plane vertically in place.
func flipRows(im *fits.Image) {
	for _, p := range im.Pix {
		for y := 0; y < im.H/2; y++ {
			a, b := y*im.W, (im.H-1-y)*im.W
			for x := 0; x < im.W; x++ {
				p[a+x], p[b+x] = p[b+x], p[a+x]
			}
		}
	}
}

func firstBase(bases ...string) string {
	for _, b := range bases {
		if b != "" {
			return b
		}
	}
	return ""
}

// finishLuminance crops the finish's luminance over the bbox (mean of channels for colour) —
// v2 uses it for statistics/telemetry only; the composite is per-channel.
func finishLuminance(finish *fits.Image, g *discGeom) []float32 {
	lum := make([]float32, g.bw*g.bh)
	for li := range lum {
		fi := g.full(li)
		if finish.C == 3 {
			lum[li] = (finish.Pix[0][fi] + finish.Pix[1][fi] + finish.Pix[2][fi]) / 3
		} else {
			lum[li] = finish.Pix[0][fi]
		}
	}
	return lum
}

// esRecord is the per-render provenance dropped beside the outputs (earthshine.json).
type esRecord struct {
	Applied       bool      `json:"applied"`
	Reason        string    `json:"reason,omitempty"`
	Gain          float64   `json:"gain"`
	Feather       float64   `json:"feather,omitempty"`
	CX            float64   `json:"cx,omitempty"`
	CY            float64   `json:"cy,omitempty"`
	R             float64   `json:"r,omitempty"`
	Inliers       int       `json:"inliers,omitempty"`
	ArcDeg        float64   `json:"arc_deg,omitempty"`
	SkyLevel      float64   `json:"sky_level,omitempty"`
	DarkMedian    float64   `json:"dark_median,omitempty"`
	FinDarkMedian float64   `json:"fin_dark_median,omitempty"`
	Target        float64   `json:"target,omitempty"`
	Knee          float64   `json:"knee,omitempty"`
	ShoulderCeil  float64   `json:"shoulder_ceil,omitempty"`
	DarkP995      float64   `json:"dark_p995,omitempty"`
	GlowLevel     float64   `json:"glow_level,omitempty"`
	GlowRingP25   float64   `json:"glow_ring_p25,omitempty"`
	FeatherSigma  float64   `json:"feather_sigma,omitempty"`
	DilatePx      int       `json:"dilate_px,omitempty"`
	Denoise       float64   `json:"denoise_strength,omitempty"`
	LitChroma     []float64 `json:"lit_chroma,omitempty"`
	ChromaMode    string    `json:"chroma_mode,omitempty"`
	Cap           float64   `json:"cap,omitempty"` // = shoulder_ceil; legacy field name kept
	GuardSigma    float64   `json:"guard_sigma,omitempty"`
}

// fillShoulder records the tone-shoulder geometry and the finish's current dark level.
func (r *esRecord) fillShoulder(sh esShoulder, finLum []float32, dark []int) {
	r.Target, r.Knee, r.ShoulderCeil, r.Cap = sh.level, sh.knee, sh.ceil, sh.ceil
	r.DarkP995 = sh.xP
	r.Denoise = esDenoiseStrength
	r.FinDarkMedian = medianOverLocal(finLum, dark)
}

// noteAndRecord writes the provenance JSON (best-effort) and returns the matching run note.
func noteAndRecord(outBase string, rec esRecord) []string {
	if data, err := json.MarshalIndent(rec, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(filepath.Dir(outBase), "earthshine.json"), data, 0o644)
	}
	if !rec.Applied {
		return []string{"earthshine skipped: " + rec.Reason}
	}
	return []string{fmt.Sprintf("earthshine: disc r=%.0f px at (%.0f,%.0f), dark side lifted to ~%.2f (gain %.1f, shoulder %.2f)",
		rec.R, rec.CX, rec.CY, rec.Target, rec.Gain, rec.ShoulderCeil)}
}
