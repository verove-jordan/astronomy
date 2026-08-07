package pipeline

import (
	"encoding/json"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// params_sun.go is the solar mode's knob model.
//
// The split into tiers is what the supervised finish and the Refine panel run on: the finish knobs
// re-render a persisted master in a second or two, while the ingest and stacking knobs mean going
// back to the source frames — minutes of work for a video group. Anything that changes the latter
// is therefore tier C and never touched by the auto-tuner.

// sunPatch is the JSON shape a caller may send.
type sunPatch struct {
	// Tier A — the finish.
	FlatStrength      *float64 `json:"flat_strength,omitempty"`
	DeconvSigma       *float64 `json:"deconv_sigma,omitempty"`
	DeconvIters       *int     `json:"deconv_iters,omitempty"`
	DeconvAuto        *bool    `json:"deconv_auto,omitempty"`
	SharpenSmall      *float64 `json:"sharpen_small,omitempty"`
	SharpenMedium     *float64 `json:"sharpen_medium,omitempty"`
	SharpenLarge      *float64 `json:"sharpen_large,omitempty"`
	SharpenDenoise    *float64 `json:"sharpen_denoise,omitempty"`
	LimbFlatten       *float64 `json:"limb_flatten,omitempty"`
	ProminenceBoost   *float64 `json:"prominence_boost,omitempty"`
	ProminenceFeather *float64 `json:"prominence_feather,omitempty"`
	Palette           *string  `json:"palette,omitempty"`
	Stretch           *float64 `json:"stretch,omitempty"`
	Contrast          *float64 `json:"contrast,omitempty"`
	Saturation        *float64 `json:"saturation,omitempty"`
	BackgroundLevel   *float64 `json:"background_level,omitempty"`
	BackgroundTint    *float64 `json:"background_tint,omitempty"`
	GlowStrength      *float64 `json:"glow_strength,omitempty"`
	GlowRadius        *float64 `json:"glow_radius,omitempty"`

	// Tier C — ingest, registration and stacking.
	KeepPercent       *int     `json:"keep_percent,omitempty"`
	MaxFrames         *int     `json:"max_frames,omitempty"`
	Drizzle           *float64 `json:"drizzle,omitempty"`
	ClipSigma         *float64 `json:"clip_sigma,omitempty"`
	WindowSeconds     *float64 `json:"window_seconds,omitempty"`
	WindowFrames      *int     `json:"window_frames,omitempty"`
	MinFrames         *int     `json:"min_frames,omitempty"`
	CropMargin        *float64 `json:"crop_margin,omitempty"`
	ScaleTolerance    *float64 `json:"scale_tolerance,omitempty"`
	RescaleGroups     *bool    `json:"rescale_groups,omitempty"`
	BracketMerge      *bool    `json:"bracket_merge,omitempty"`
	BracketStops      *float64 `json:"bracket_stops,omitempty"`
	TransparencyFloor *float64 `json:"transparency_floor,omitempty"`
	APAlign           *bool    `json:"ap_align,omitempty"`
	APScale           *int     `json:"ap_scale,omitempty"`
	Band              *string  `json:"band,omitempty"`
}

// applySunParamPatch is the solar patch model.
func applySunParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	var patch sunPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return working, tierA, false
	}
	next := working
	s := &next.Sun
	f := &s.Finish

	setF(&f.FlatStrength, patch.FlatStrength)
	setF(&f.DeconvSigma, patch.DeconvSigma)
	setI(&f.DeconvIters, patch.DeconvIters)
	// Naming a width is how a user turns the measurement off: the two settings answer the same
	// question, and silently measuring over an explicit number would make the knob look broken.
	if patch.DeconvSigma != nil {
		f.DeconvAuto = false
	}
	setB(&f.DeconvAuto, patch.DeconvAuto)
	setF(&f.LimbFlatten, patch.LimbFlatten)
	setF(&f.ProminenceBoost, patch.ProminenceBoost)
	setF(&f.ProminenceFeather, patch.ProminenceFeather)
	setF(&f.Stretch, patch.Stretch)
	setF(&f.Contrast, patch.Contrast)
	setF(&f.Saturation, patch.Saturation)
	setF(&f.BackgroundLevel, patch.BackgroundLevel)
	setF(&f.BackgroundTint, patch.BackgroundTint)
	setF(&f.GlowStrength, patch.GlowStrength)
	setF(&f.GlowRadius, patch.GlowRadius)
	if patch.Palette != nil {
		if p := strings.ToLower(strings.TrimSpace(*patch.Palette)); solar.IsPalette(p) {
			f.Palette = p
		}
	}
	applySharpenGroups(f, patch)

	setI(&s.KeepPercent, patch.KeepPercent)
	setI(&s.MaxFrames, patch.MaxFrames)
	setF(&s.Drizzle, patch.Drizzle)
	setF(&s.ClipSigma, patch.ClipSigma)
	setF(&s.WindowSeconds, patch.WindowSeconds)
	setI(&s.WindowFrames, patch.WindowFrames)
	setI(&s.MinFrames, patch.MinFrames)
	setF(&s.CropMargin, patch.CropMargin)
	setF(&s.ScaleTolerance, patch.ScaleTolerance)
	setB(&s.RescaleGroups, patch.RescaleGroups)
	setB(&s.BracketMerge, patch.BracketMerge)
	setF(&s.BracketStops, patch.BracketStops)
	setF(&s.TransparencyFloor, patch.TransparencyFloor)
	setB(&s.APAlign, patch.APAlign)
	setI(&s.APScale, patch.APScale)
	if patch.Band != nil {
		if b := solar.Band(strings.ToLower(strings.TrimSpace(*patch.Band))); isSunBand(b) {
			s.Band = b
		}
	}
	next = clampSun(next)

	t := tierA
	if sunStackChanged(working.Sun, next.Sun) {
		t = tierC
	}
	return next, t, t == tierC || sunFinishChanged(working.Sun.Finish, next.Sun.Finish)
}

// applySharpenGroups maps the three user-facing sharpening controls onto the five starlet scales.
//
// Exposing five raw per-scale gains would be a more faithful control surface and a worse one: the
// scales are not independently meaningful to a user, and a five-dimensional space is more than the
// supervised auto-tuner can search usefully. Grouping them into fine, medium and large keeps the
// knobs interpretable and the search tractable.
func applySharpenGroups(f *solar.FinishOptions, patch sunPatch) {
	if len(f.Sharpen.Gains) < 5 {
		f.Sharpen = solar.DefaultSharpen(f.DeconvSigma)
	}
	if patch.SharpenSmall != nil {
		f.Sharpen.Gains[0], f.Sharpen.Gains[1] = *patch.SharpenSmall*0.8/1.15, *patch.SharpenSmall
	}
	if patch.SharpenMedium != nil {
		f.Sharpen.Gains[2], f.Sharpen.Gains[3] = *patch.SharpenMedium, *patch.SharpenMedium*1.25/1.35
	}
	if patch.SharpenLarge != nil {
		f.Sharpen.Gains[4] = *patch.SharpenLarge
	}
	if patch.SharpenDenoise != nil {
		k := clampf(*patch.SharpenDenoise, 0, 1) * 2
		f.Sharpen.Thresholds = []float64{4 * k, 2 * k, 1 * k, 0, 0}
	}
}

// sharpenGroup reads a grouped sharpening control back out of the per-scale gains.
func sharpenGroup(gains []float64, group int) float64 {
	idx := []int{1, 2, 4}
	if group < 0 || group >= len(idx) || idx[group] >= len(gains) {
		return 0
	}
	return gains[idx[group]]
}

// sharpenDenoise reads the grouped denoise control back out of the thresholds.
func sharpenDenoise(thr []float64) float64 {
	if len(thr) == 0 || thr[0] <= 0 {
		return 0
	}
	return clampf(thr[0]/8, 0, 1)
}

// clampSun bounds every solar knob to a range that produces an image rather than an artefact.
func clampSun(p mode.Preset) mode.Preset {
	s := &p.Sun
	f := &s.Finish
	f.FlatStrength = clampf(f.FlatStrength, 0, 1)
	f.DeconvSigma = clampf(f.DeconvSigma, 0, 5)
	// The ceiling is above the default of 50, not below it. It used to be 30, which meant that
	// sending ANY sun patch — a palette change, a stretch nudge — quietly cut the iteration count
	// nearly in half and re-rendered a softer image than the run had produced.
	f.DeconvIters = clampi(f.DeconvIters, 0, 80)
	f.LimbFlatten = clampf(f.LimbFlatten, 0, 1)
	f.ProminenceBoost = clampf(f.ProminenceBoost, 0, 4)
	f.ProminenceFeather = clampf(f.ProminenceFeather, 0, 0.05)
	f.Stretch = clampf(f.Stretch, 0, 1)
	f.Contrast = clampf(f.Contrast, 0.2, 3)
	f.Saturation = clampf(f.Saturation, 0, 2)
	f.BackgroundLevel = clampf(f.BackgroundLevel, 0, 0.3)
	f.BackgroundTint = clampf(f.BackgroundTint, 0, 1)
	// The glow's ceiling is 1, not more, and that is load-bearing rather than tidy: it is stated as a
	// fraction of what the limb itself renders at, and the finished image staying monotone across the
	// limb depends on the halo never exceeding it (addDiscGlow).
	f.GlowStrength = clampf(f.GlowStrength, 0, 1)
	f.GlowRadius = clampf(f.GlowRadius, 0, 0.3)
	for i := range f.Sharpen.Gains {
		f.Sharpen.Gains[i] = clampf(f.Sharpen.Gains[i], 0, 4)
	}
	s.KeepPercent = clampi(s.KeepPercent, 5, 100)
	s.MaxFrames = clampi(s.MaxFrames, 8, 2000)
	s.Drizzle = clampf(s.Drizzle, 1, 2)
	s.ClipSigma = clampf(s.ClipSigma, 1, 6)
	s.WindowSeconds = clampf(s.WindowSeconds, 5, 3600)
	s.WindowFrames = clampi(s.WindowFrames, 8, 2000)
	s.MinFrames = clampi(s.MinFrames, 2, 500)
	s.CropMargin = clampf(s.CropMargin, 0.02, 1)
	s.ScaleTolerance = clampf(s.ScaleTolerance, 0.002, 0.2)
	// Both of these take 0 to mean "the built-in behaviour" — no tiering gap of its own, and no
	// transparency gate — so the floor of the range has to be 0 rather than the smallest sensible
	// working value, or that meaning becomes unreachable through the knob.
	s.BracketStops = clampf(s.BracketStops, 0, 6)
	s.TransparencyFloor = clampf(s.TransparencyFloor, 0, 1)
	s.APScale = clampi(s.APScale, 0, 8)
	return p
}

// isSunBand reports whether b is a band the ingest understands.
func isSunBand(b solar.Band) bool {
	return b == solar.BandAuto || b == solar.BandHAlpha || b == solar.BandWhiteLight
}

// sunStackChanged reports whether anything upstream of the finish moved.
func sunStackChanged(a, b solar.Preset) bool {
	return a.KeepPercent != b.KeepPercent || a.MaxFrames != b.MaxFrames ||
		floatChanged(a.Drizzle, b.Drizzle) || floatChanged(a.ClipSigma, b.ClipSigma) ||
		floatChanged(a.WindowSeconds, b.WindowSeconds) || a.WindowFrames != b.WindowFrames ||
		a.MinFrames != b.MinFrames || floatChanged(a.CropMargin, b.CropMargin) ||
		floatChanged(a.ScaleTolerance, b.ScaleTolerance) || a.Band != b.Band ||
		a.RescaleGroups != b.RescaleGroups || a.BracketMerge != b.BracketMerge ||
		floatChanged(a.BracketStops, b.BracketStops) ||
		floatChanged(a.TransparencyFloor, b.TransparencyFloor) ||
		a.APAlign != b.APAlign || a.APScale != b.APScale
}

// sunFinishChanged reports whether the finish would render differently.
func sunFinishChanged(a, b solar.FinishOptions) bool {
	if a.Palette != b.Palette || a.DeconvIters != b.DeconvIters || a.DeconvAuto != b.DeconvAuto {
		return true
	}
	for i := range a.Sharpen.Gains {
		if i < len(b.Sharpen.Gains) && floatChanged(a.Sharpen.Gains[i], b.Sharpen.Gains[i]) {
			return true
		}
	}
	return floatChanged(a.FlatStrength, b.FlatStrength) || floatChanged(a.DeconvSigma, b.DeconvSigma) ||
		floatChanged(a.LimbFlatten, b.LimbFlatten) || floatChanged(a.ProminenceBoost, b.ProminenceBoost) ||
		floatChanged(a.ProminenceFeather, b.ProminenceFeather) || floatChanged(a.Stretch, b.Stretch) ||
		floatChanged(a.Contrast, b.Contrast) || floatChanged(a.Saturation, b.Saturation) ||
		floatChanged(a.BackgroundLevel, b.BackgroundLevel) || floatChanged(a.BackgroundTint, b.BackgroundTint) ||
		floatChanged(a.GlowStrength, b.GlowStrength) || floatChanged(a.GlowRadius, b.GlowRadius)
}

// sunKnobMenu is the finish surface the supervisor may tune, and the text its critique prompt
// embeds. Only tier-A knobs appear: re-stacking a solar group means re-reading thousands of frames,
// which is not something an auto-tuner should reach for between renders.
const sunKnobMenu = `flat_strength 0..1 — removes the etalon's rings, dust and sweet-spot gradient
deconv_sigma 0..5 — Gaussian PSF width for deconvolution; 0 turns it off, and setting it at all
  overrides the width measured from the limb
deconv_auto true|false — measure the PSF width off the limb instead of naming it (on by default)
deconv_iters 0..80 — deconvolution iterations; more sharpens further and rings sooner
sharpen_small 0..4 — fine-scale contrast (granulation, filament edges); above ~1.5 it amplifies noise
sharpen_medium 0..4 — mid-scale contrast (filaments, plage), the main detail control
sharpen_large 0..4 — large-scale contrast (active regions); near 1 unless the disc looks flat
sharpen_denoise 0..1 — suppresses noise at the finest scales before the gains are applied
limb_flatten 0..1 — removes limb darkening so detail reads to the edge; 0 keeps the natural falloff
prominence_boost 0..4 — brightness of the off-limb prominences relative to the disc
prominence_feather 0..0.05 — how softly the disc and the off-limb rendering are blended
palette gold|neutral|mono|inverted — the colour rendering
stretch 0..1 — midtone lift; higher shows more of the faint surface detail
contrast 0.2..3 — overall contrast
saturation 0..2 — colour strength of the palette
background_level 0..0.3 — how bright the sky renders; 0 is black, ~0.05 the warm pedestal the
  reference images have
background_tint 0..1 — how much of the palette's own hue the sky carries; 0 neutral grey, 1 amber
glow_strength 0..1 — brightness of the halo around the disc, as a fraction of what the limb renders at
glow_radius 0..0.3 — how far that halo reaches, as a fraction of the disc radius`
