// Parameter model for the local-AI-agent supervised finish. The model proposes a patch to a curated
// whitelist of mode.Preset fields; each field is tagged by the pipeline re-entry tier its change
// requires, so the loop can re-run only the cheapest stage that reflects the change. See supervise.go
// for the loop and supervise_reentry.go for how each tier is rendered.
package pipeline

import (
	"math"
	"strings"

	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// tier is the re-entry cost level a parameter change requires (higher re-runs more, costs more).
type tier int

const (
	tierA tier = iota // composite only: re-render the GIMP composite (seconds)
	tierB             // finish/prep: re-run the linear prep (stretch/SPCC/background/denoise) then composite (tens of s–min)
	tierC             // stack: re-stack from the raw frames, then prep + composite (min–hours)
)

// Per-tier iteration budgets on top of superviseHardMaxIters: the expensive tiers are capped hard so
// full autonomy can never blow up wall-clock. Once a tier's budget is spent it is dropped from the
// model's menu (see the loop and the critique prompt).
const (
	superviseBudgetTierB = 3
	superviseBudgetTierC = 2
)

func (t tier) String() string {
	switch t {
	case tierC:
		return "C"
	case tierB:
		return "B"
	default:
		return "A"
	}
}

// parseTier reads a tier ceiling ("A"/"B"/"C", case-insensitive); anything else → tierA.
func parseTier(s string) tier {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "C":
		return tierC
	case "B":
		return tierB
	default:
		return tierA
	}
}

// composeParams are the Tier-A GIMP-composite knobs, applied on every render (cheap). They are read
// from the working preset each iteration so a composite-only change never re-runs the linear prep.
type composeParams struct {
	Saturation        float64
	HaScreen          float64
	HaBlackPoint      float64
	OIIIScreen        float64
	OIIIBlackPoint    float64
	SIIScreen         float64
	SIIBlackPoint     float64
	SIITint           string
	ChromaBlur        float64
	CropFrac          float64
	Curve             []float64
	LumCurve          []float64
	LumOpacity        float64
	CoreHighlightKnee float64
	CoreHighlightCeil float64
	HighlightKnee     float64
	HighlightCeil     float64
	StarDesat         float64
	HaExcludeStars    bool
}

func presetComposeParams(p *mode.Preset) composeParams {
	return composeParams{
		Saturation:        p.Saturation,
		HaScreen:          p.HaScreen,
		HaBlackPoint:      p.HaBlackPoint,
		OIIIScreen:        p.OIIIScreen,
		OIIIBlackPoint:    p.OIIIBlackPoint,
		SIIScreen:         p.SIIScreen,
		SIIBlackPoint:     p.SIIBlackPoint,
		SIITint:           p.SIITint,
		ChromaBlur:        p.ChromaBlur,
		CropFrac:          p.CropFrac,
		Curve:             p.Curve,
		LumCurve:          p.LumCurve,
		LumOpacity:        p.LumOpacity,
		CoreHighlightKnee: p.CoreHighlightKnee,
		CoreHighlightCeil: p.CoreHighlightCeil,
		HighlightKnee:     p.HighlightKnee,
		HighlightCeil:     p.HighlightCeil,
		StarDesat:         p.StarDesat,
		HaExcludeStars:    p.HaExcludeStars,
	}
}

// supervisePatch is the model's proposed change — every field optional (a nil pointer leaves that
// knob untouched), so a partial reply overrides only the knobs the model names. Fields are grouped by
// the tier their change triggers.
type supervisePatch struct {
	// Tier A — GIMP composite (seconds).
	Saturation        *float64 `json:"saturation,omitempty"`
	HaScreen          *float64 `json:"ha_screen,omitempty"`
	HaBlackPoint      *float64 `json:"ha_black_point,omitempty"`
	OIIIScreen        *float64 `json:"oiii_screen,omitempty"`
	OIIIBlackPoint    *float64 `json:"oiii_black_point,omitempty"`
	SIIScreen         *float64 `json:"sii_screen,omitempty"`
	SIIBlackPoint     *float64 `json:"sii_black_point,omitempty"`
	SIITint           *string  `json:"sii_tint,omitempty"`
	ChromaBlur        *float64 `json:"chroma_blur,omitempty"`
	LumOpacity        *float64 `json:"lum_opacity,omitempty"`
	LumBoost          *float64 `json:"lum_boost,omitempty"`
	CropFrac          *float64 `json:"crop_frac,omitempty"`
	CoreHighlightKnee *float64 `json:"core_highlight_knee,omitempty"`
	CoreHighlightCeil *float64 `json:"core_highlight_ceil,omitempty"`
	HighlightKnee     *float64 `json:"highlight_knee,omitempty"`
	HighlightCeil     *float64 `json:"highlight_ceil,omitempty"`
	StarDesat         *float64 `json:"star_desat,omitempty"`
	HaExcludeStars    *bool    `json:"ha_exclude_stars,omitempty"`

	// Tier B — linear finish prep (tens of s–min).
	BackgroundLevel      *float64 `json:"background_level,omitempty"`
	LinkedStretch        *bool    `json:"linked_stretch,omitempty"`
	ColorCalibration     *bool    `json:"color_calibration,omitempty"`
	CombinedBackgroundAI *bool    `json:"combined_background_ai,omitempty"`
	BackgroundDegree     *int     `json:"background_degree,omitempty"`
	ColorDenoiseAI       *bool    `json:"color_denoise_ai,omitempty"`
	ChromaSmoothPx       *int     `json:"chroma_smooth_px,omitempty"`
	ChromaBgSmoothPx     *int     `json:"chroma_bg_smooth_px,omitempty"`
	SkyChromaFlattenPx   *int     `json:"sky_chroma_flatten_px,omitempty"`
	SkyLumFlattenPx      *int     `json:"sky_lum_flatten_px,omitempty"`
	StarReduce           *float64 `json:"star_reduce,omitempty"`
	StretchHeadroom      *float64 `json:"stretch_headroom,omitempty"`
	Palette              *string  `json:"palette,omitempty"`
	HaContinuumSub       *bool    `json:"ha_continuum_sub,omitempty"`
	// Mono side-output toggles (deliverable flags, deliberately absent from the VLM knob menu).
	// Tier B: the monos are rendered by the finish, which a Tier-B re-entry re-runs.
	EmitLuminanceMono  *bool `json:"emit_luminance_mono,omitempty"`
	EmitAllChannelMono *bool `json:"emit_all_channel_mono,omitempty"`

	// Tier C — re-stack from the raw frames (min–hours).
	RoundnessFloor  *float64 `json:"roundness_floor,omitempty"`
	FWHMSigma       *float64 `json:"fwhm_sigma,omitempty"`
	BackgroundSigma *float64 `json:"background_sigma,omitempty"`
	StarCountFrac   *float64 `json:"star_count_frac,omitempty"`
	TrailMaskK      *float64 `json:"trail_mask_k,omitempty"`
	DenoiseChroma   *float64 `json:"denoise_chroma,omitempty"`
	DenoiseLum      *float64 `json:"denoise_lum,omitempty"`
	BackgroundAI    *bool    `json:"background_ai,omitempty"`
	SeamOffsetRefit *bool    `json:"seam_offset_refit,omitempty"`
	SeamNoiseEq     *bool    `json:"seam_noise_eq,omitempty"`
	// The multi-night union-canvas knob. Current wire key: union_canvas; "mosaic" is the legacy
	// alias (kept readable so old presets/params keep applying) — when both are present the
	// current key wins. The tiled-panel MODE "mosaic" is unrelated.
	UnionCanvas     *bool   `json:"union_canvas,omitempty"`
	UnionCanvasFill *string `json:"union_canvas_fill,omitempty"`
	Mosaic          *bool   `json:"mosaic,omitempty"`
	MosaicFill      *string `json:"mosaic_fill,omitempty"`
}

// apply overrides only the set fields of a copy of p, leaving the rest of the preset untouched.
func (patch supervisePatch) apply(p mode.Preset) mode.Preset {
	setF(&p.Saturation, patch.Saturation)
	setF(&p.HaScreen, patch.HaScreen)
	setF(&p.HaBlackPoint, patch.HaBlackPoint)
	setF(&p.OIIIScreen, patch.OIIIScreen)
	setF(&p.OIIIBlackPoint, patch.OIIIBlackPoint)
	setF(&p.SIIScreen, patch.SIIScreen)
	setF(&p.SIIBlackPoint, patch.SIIBlackPoint)
	setF(&p.ChromaBlur, patch.ChromaBlur)
	setF(&p.LumOpacity, patch.LumOpacity)
	setF(&p.LumBoost, patch.LumBoost)
	setF(&p.CropFrac, patch.CropFrac)
	setF(&p.CoreHighlightKnee, patch.CoreHighlightKnee)
	setF(&p.CoreHighlightCeil, patch.CoreHighlightCeil)
	setF(&p.HighlightKnee, patch.HighlightKnee)
	setF(&p.HighlightCeil, patch.HighlightCeil)
	setF(&p.StarDesat, patch.StarDesat)
	setB(&p.HaExcludeStars, patch.HaExcludeStars)

	setF(&p.BackgroundLevel, patch.BackgroundLevel)
	setB(&p.LinkedStretch, patch.LinkedStretch)
	setB(&p.ColorCalibration, patch.ColorCalibration)
	setB(&p.CombinedBackgroundAI, patch.CombinedBackgroundAI)
	setI(&p.BackgroundDegree, patch.BackgroundDegree)
	setB(&p.ColorDenoiseAI, patch.ColorDenoiseAI)
	setI(&p.ChromaSmoothPx, patch.ChromaSmoothPx)
	setI(&p.ChromaBgSmoothPx, patch.ChromaBgSmoothPx)
	setI(&p.SkyChromaFlattenPx, patch.SkyChromaFlattenPx)
	setI(&p.SkyLumFlattenPx, patch.SkyLumFlattenPx)
	setF(&p.StarReduce, patch.StarReduce)
	setF(&p.StretchHeadroom, patch.StretchHeadroom)
	setB(&p.HaContinuumSub, patch.HaContinuumSub)
	setB(&p.EmitLuminanceMono, patch.EmitLuminanceMono)
	setB(&p.EmitAllChannelMono, patch.EmitAllChannelMono)
	if patch.Palette != nil { // string enum — validate against the known palettes (like nightscape Look)
		if s := strings.ToLower(strings.TrimSpace(*patch.Palette)); isPaletteName(s) {
			p.Palette = s
		}
	}
	if patch.SIITint != nil { // string enum, same idiom as Palette
		if s := strings.ToLower(strings.TrimSpace(*patch.SIITint)); mode.IsSIITint(s) {
			p.SIITint = s
		}
	}

	setF(&p.Grade.RoundnessFloor, patch.RoundnessFloor)
	setF(&p.Grade.FWHMSigma, patch.FWHMSigma)
	setF(&p.Grade.BackgroundSigma, patch.BackgroundSigma)
	setF(&p.Grade.StarCountFrac, patch.StarCountFrac)
	setF(&p.TrailMaskK, patch.TrailMaskK)
	setF(&p.DenoiseChroma, patch.DenoiseChroma)
	setF(&p.DenoiseLum, patch.DenoiseLum)
	setB(&p.BackgroundAI, patch.BackgroundAI)
	setB(&p.SeamOffsetRefit, patch.SeamOffsetRefit)
	setB(&p.SeamNoiseEq, patch.SeamNoiseEq)
	setB(&p.Mosaic, patch.Mosaic)      // legacy alias first …
	setB(&p.Mosaic, patch.UnionCanvas) // … the current key wins
	setS(&p.MosaicFill, patch.MosaicFill)
	setS(&p.MosaicFill, patch.UnionCanvasFill)
	return p
}

// clampPreset bounds every whitelisted knob to the range the pipeline actually honours, so a wild
// model suggestion can never push processing outside known-good territory.
func clampPreset(p mode.Preset) mode.Preset {
	if p.MosaicFill != "" && p.MosaicFill != "crop" && p.MosaicFill != "fill" {
		p.MosaicFill = "crop" // unknown policy → the safe default (no synthetic sky in the export)
	}
	// Tier A.
	p.Saturation = clampf(p.Saturation, 0, 0.35) // capped below the old 0.6 — high satu split stars into a garish blue-purple / orange look
	p.HaScreen = clampf(p.HaScreen, 0, 0.8)
	p.HaBlackPoint = clampf(p.HaBlackPoint, 0, 0.3)
	p.OIIIScreen = clampf(p.OIIIScreen, 0, 0.8)
	p.OIIIBlackPoint = clampf(p.OIIIBlackPoint, 0, 0.3)
	p.SIIScreen = clampf(p.SIIScreen, 0, 0.8)
	p.SIIBlackPoint = clampf(p.SIIBlackPoint, 0, 0.3)
	if !mode.IsSIITint(p.SIITint) {
		p.SIITint = "" // unknown tint → the default deep red
	}
	p.ChromaBlur = clampf(p.ChromaBlur, 0, 12)
	p.LumOpacity = clampf(p.LumOpacity, 0, 1)
	p.LumBoost = clampf(p.LumBoost, 0, 0.25)
	p.CropFrac = clampf(p.CropFrac, 0, 0.1)
	if p.CoreHighlightKnee != 0 || p.CoreHighlightCeil != 0 {
		p.CoreHighlightKnee = clampf(p.CoreHighlightKnee, 0, 0.95)
		p.CoreHighlightCeil = clampf(p.CoreHighlightCeil, 0, 0.99)
	}
	if p.HighlightKnee != 0 || p.HighlightCeil != 0 {
		p.HighlightKnee = clampf(p.HighlightKnee, 0, 0.98)
		p.HighlightCeil = clampf(p.HighlightCeil, 0, 0.995)
	}
	p.StarDesat = clampf(p.StarDesat, 0, 1)
	// Tier B.
	p.BackgroundLevel = clampf(p.BackgroundLevel, 0.03, 0.2)
	p.BackgroundDegree = clampi(p.BackgroundDegree, 1, 4)
	p.ChromaSmoothPx = clampi(p.ChromaSmoothPx, 0, 16)
	p.ChromaBgSmoothPx = clampi(p.ChromaBgSmoothPx, 0, 64)
	p.SkyChromaFlattenPx = clampi(p.SkyChromaFlattenPx, 0, 128)
	p.SkyLumFlattenPx = clampi(p.SkyLumFlattenPx, 0, 256)
	p.StarReduce = clampf(p.StarReduce, 0, 1)
	if p.StretchHeadroom != 0 { // 0 = off; otherwise keep it a genuine sub-1.0 cap with usable range
		p.StretchHeadroom = clampf(p.StretchHeadroom, 0.7, 1.0)
	}
	if p.Palette != "" { // normalize + drop an unknown palette back to natural (safety net beyond apply)
		p.Palette = strings.ToLower(strings.TrimSpace(p.Palette))
		if !isPaletteName(p.Palette) {
			p.Palette = ""
		}
	}
	// Tier C.
	p.Grade.RoundnessFloor = clampf(p.Grade.RoundnessFloor, 0.2, 0.95)
	p.Grade.FWHMSigma = clampf(p.Grade.FWHMSigma, 1, 5)
	p.Grade.BackgroundSigma = clampf(p.Grade.BackgroundSigma, 1, 5)
	p.Grade.StarCountFrac = clampf(p.Grade.StarCountFrac, 0.1, 1)
	p.TrailMaskK = clampf(p.TrailMaskK, 0, 6)
	p.DenoiseChroma = clampf(p.DenoiseChroma, 0, 1)
	p.DenoiseLum = clampf(p.DenoiseLum, 0, 1)
	return p
}

// tierOf returns the highest-cost tier whose fields differ between prev and next — the stage the
// pipeline must re-enter to reflect the change. A pure Tier-A composite tweak returns tierA.
func tierOf(prev, next mode.Preset) tier {
	if gradeChanged(prev.Grade, next.Grade) ||
		floatChanged(prev.TrailMaskK, next.TrailMaskK) ||
		floatChanged(prev.DenoiseChroma, next.DenoiseChroma) ||
		floatChanged(prev.DenoiseLum, next.DenoiseLum) ||
		prev.BackgroundAI != next.BackgroundAI ||
		prev.SeamOffsetRefit != next.SeamOffsetRefit ||
		prev.SeamNoiseEq != next.SeamNoiseEq ||
		prev.Mosaic != next.Mosaic ||
		prev.MosaicFill != next.MosaicFill {
		return tierC
	}
	if floatChanged(prev.BackgroundLevel, next.BackgroundLevel) ||
		prev.LinkedStretch != next.LinkedStretch ||
		prev.ColorCalibration != next.ColorCalibration ||
		prev.CombinedBackgroundAI != next.CombinedBackgroundAI ||
		prev.BackgroundDegree != next.BackgroundDegree ||
		prev.ColorDenoiseAI != next.ColorDenoiseAI ||
		prev.ChromaSmoothPx != next.ChromaSmoothPx ||
		prev.ChromaBgSmoothPx != next.ChromaBgSmoothPx ||
		prev.SkyChromaFlattenPx != next.SkyChromaFlattenPx ||
		prev.SkyLumFlattenPx != next.SkyLumFlattenPx ||
		floatChanged(prev.StarReduce, next.StarReduce) ||
		floatChanged(prev.StretchHeadroom, next.StretchHeadroom) ||
		prev.Palette != next.Palette ||
		prev.HaContinuumSub != next.HaContinuumSub ||
		prev.EmitLuminanceMono != next.EmitLuminanceMono ||
		prev.EmitAllChannelMono != next.EmitAllChannelMono {
		return tierB
	}
	return tierA
}

// composeChanged reports whether any Tier-A composite knob differs — used with tierOf to detect a
// no-op patch (the loop's convergence stop).
func composeChanged(prev, next mode.Preset) bool {
	return floatChanged(prev.Saturation, next.Saturation) ||
		floatChanged(prev.HaScreen, next.HaScreen) ||
		floatChanged(prev.HaBlackPoint, next.HaBlackPoint) ||
		floatChanged(prev.OIIIScreen, next.OIIIScreen) ||
		floatChanged(prev.OIIIBlackPoint, next.OIIIBlackPoint) ||
		floatChanged(prev.SIIScreen, next.SIIScreen) ||
		floatChanged(prev.SIIBlackPoint, next.SIIBlackPoint) ||
		prev.SIITint != next.SIITint ||
		floatChanged(prev.ChromaBlur, next.ChromaBlur) ||
		floatChanged(prev.LumOpacity, next.LumOpacity) ||
		floatChanged(prev.LumBoost, next.LumBoost) ||
		floatChanged(prev.CropFrac, next.CropFrac) ||
		floatChanged(prev.CoreHighlightKnee, next.CoreHighlightKnee) ||
		floatChanged(prev.CoreHighlightCeil, next.CoreHighlightCeil) ||
		floatChanged(prev.HighlightKnee, next.HighlightKnee) ||
		floatChanged(prev.HighlightCeil, next.HighlightCeil) ||
		floatChanged(prev.StarDesat, next.StarDesat) ||
		prev.HaExcludeStars != next.HaExcludeStars
}

func gradeChanged(a, b grade.Options) bool {
	return floatChanged(a.RoundnessFloor, b.RoundnessFloor) ||
		floatChanged(a.FWHMSigma, b.FWHMSigma) ||
		floatChanged(a.BackgroundSigma, b.BackgroundSigma) ||
		floatChanged(a.StarCountFrac, b.StarCountFrac)
}

// paramsMap flattens the numeric knobs the model tuned into a flat map for the persisted iteration
// record (UI-friendly), so the supervisor panel can show what each pass used.
func paramsMap(p mode.Preset) map[string]float64 {
	return map[string]float64{
		"saturation":            p.Saturation,
		"ha_screen":             p.HaScreen,
		"ha_black_point":        p.HaBlackPoint,
		"oiii_screen":           p.OIIIScreen,
		"oiii_black_point":      p.OIIIBlackPoint,
		"sii_screen":            p.SIIScreen,
		"sii_black_point":       p.SIIBlackPoint,
		"chroma_blur":           p.ChromaBlur,
		"lum_boost":             p.LumBoost,
		"crop_frac":             p.CropFrac,
		"star_desat":            p.StarDesat,
		"background_level":      p.BackgroundLevel,
		"background_degree":     float64(p.BackgroundDegree),
		"chroma_smooth_px":      float64(p.ChromaSmoothPx),
		"chroma_bg_smooth_px":   float64(p.ChromaBgSmoothPx),
		"sky_chroma_flatten_px": float64(p.SkyChromaFlattenPx),
		"sky_lum_flatten_px":    float64(p.SkyLumFlattenPx),
		"star_reduce":           p.StarReduce,
		"stretch_headroom":      p.StretchHeadroom,
	}
}

// affordableTier is the highest tier the loop can still render given the tier ceiling and the
// remaining per-tier budgets: an exhausted Tier-C budget caps at B, an exhausted Tier-B budget caps
// at A. Tier A (the fast composite) is always affordable.
func affordableTier(ceiling tier, budgetB, budgetC int) tier {
	t := ceiling
	if t >= tierC && budgetC <= 0 {
		t = tierB
	}
	if t >= tierB && budgetB <= 0 {
		t = tierA
	}
	return t
}

// capToTier reverts any fields above tier t in cand back to base, so the working preset never carries
// a change the loop cannot afford to render (which would silently drift from the shown image).
func capToTier(base, cand mode.Preset, t tier) mode.Preset {
	if t < tierC {
		cand.Grade = base.Grade
		cand.TrailMaskK = base.TrailMaskK
		cand.DenoiseChroma = base.DenoiseChroma
		cand.DenoiseLum = base.DenoiseLum
		cand.BackgroundAI = base.BackgroundAI
	}
	if t < tierB {
		cand.BackgroundLevel = base.BackgroundLevel
		cand.LinkedStretch = base.LinkedStretch
		cand.ColorCalibration = base.ColorCalibration
		cand.CombinedBackgroundAI = base.CombinedBackgroundAI
		cand.BackgroundDegree = base.BackgroundDegree
		cand.ColorDenoiseAI = base.ColorDenoiseAI
		cand.ChromaSmoothPx = base.ChromaSmoothPx
		cand.ChromaBgSmoothPx = base.ChromaBgSmoothPx
		cand.StarReduce = base.StarReduce
		cand.StretchHeadroom = base.StretchHeadroom
		cand.Palette = base.Palette
		cand.HaContinuumSub = base.HaContinuumSub
	}
	return cand
}

func floatChanged(a, b float64) bool { return math.Abs(a-b) > 1e-9 }

func setF(dst, v *float64) {
	if v != nil {
		*dst = *v
	}
}

func setB(dst, v *bool) {
	if v != nil {
		*dst = *v
	}
}

func setI(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}

func setS(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
