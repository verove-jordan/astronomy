package skyplan

import (
	"fmt"
	"math"
	"strings"
)

// Scoring tunables. They are package constants so the model is transparent and easy to adjust.
const (
	altIdealDeg       = 60.0  // altitude at/above which the altitude score saturates
	idealImagingHours = 5.0   // dark hours above the minimum altitude that saturate the dark-time score
	fillTinyFloor     = 0.10  // framing score for a target far too small for the frame
	fillLowGood       = 0.10  // framing plateau lower bound (fraction of the min FOV)
	fillHighGood      = 0.60  // framing plateau upper bound
	fillTightScore    = 0.35  // framing score where the target just fills the frame
	fillOversizeFloor = 0.20  // framing score once the target overflows the frame
	refApertureMM     = 100.0 // reference aperture for the detectability normalization
	baseLimitSB       = 14.0  // surface brightness (mag/arcmin²) reachable by the reference aperture
	sbRange           = 6.0   // SB span mapping to the detectability ramp
	moonSepMinDeg     = 10.0  // separation below which Moon proximity is fully penalized
	moonSepSafeDeg    = 90.0  // separation beyond which the Moon barely matters

	// Visual (eyepiece) detectability tunables.
	visualLimitBase     = 7.5  // point-source limiting magnitude scaled to a 10 mm aperture
	visualPointRange    = 4.0  // magnitude span mapping to the point-source detectability ramp
	visualContrastRange = 5.0  // surface-brightness contrast span (mag/arcmin²) for the extended ramp
	skyDarkSBArcmin     = 12.9 // dark-sky V surface brightness (mag/arcmin², ≈ 21.8 mag/arcsec²)
)

// altitudeScore maps an altitude (max-in-darkness or current) to [0,1]: 0 at the minimum altitude,
// 1 at altIdealDeg and above.
func altitudeScore(altDeg, minAltDeg float64) float64 {
	if altIdealDeg <= minAltDeg {
		if altDeg >= minAltDeg {
			return 1
		}
		return 0
	}
	return clamp01((altDeg - minAltDeg) / (altIdealDeg - minAltDeg))
}

// darkHoursScore maps dark hours above the minimum altitude to [0,1], relative to the shorter of
// idealImagingHours and the night length (so a short summer night is not unfairly penalized).
func darkHoursScore(darkHours, windowHours float64) float64 {
	denom := idealImagingHours
	if windowHours > 0 && windowHours < denom {
		denom = windowHours
	}
	if denom <= 0 {
		return 0
	}
	return clamp01(darkHours / denom)
}

// framingScore rates how well a target of the given diameter fills a sensor whose smaller dimension
// is fovMinArcmin. It plateaus when the target fills 10–60% of the frame, ramps down for tiny or
// oversized targets, and returns a neutral, flagged 0.5 when the diameter is unknown.
func framingScore(diamArcmin, fovMinArcmin float64, known bool) (score float64, isKnown bool) {
	if !known || diamArcmin <= 0 || fovMinArcmin <= 0 {
		return 0.5, false
	}
	f := diamArcmin / fovMinArcmin
	switch {
	case f < 0.02:
		return fillTinyFloor, true
	case f < fillLowGood:
		return fillTinyFloor + (1-fillTinyFloor)*(f-0.02)/(fillLowGood-0.02), true
	case f <= fillHighGood:
		return 1.0, true
	case f <= 1.0:
		return 1.0 - (1.0-fillTightScore)*(f-fillHighGood)/(1.0-fillHighGood), true
	default:
		return fillOversizeFloor, true
	}
}

// detectabilityScore folds a target's surface brightness with the aperture's light grasp. Bigger
// apertures reach fainter surface brightness; fainter targets score lower. Returns a neutral, flagged
// 0.5 when magnitude or diameter is unknown.
func detectabilityScore(magV, diamArcmin, apertureMM float64, known bool) (score float64, isKnown bool) {
	if !known || diamArcmin <= 0 || apertureMM <= 0 {
		return 0.5, false
	}
	area := math.Pi * (diamArcmin / 2) * (diamArcmin / 2)
	sb := surfaceBrightness(magV, area)
	effLim := baseLimitSB + 5*math.Log10(apertureMM/refApertureMM)
	return clamp01((effLim-sb)/sbRange + 0.5), true
}

// visualLimitMag is the faintest magnitude an aperture shows the eye for a point-like source:
// ≈ 7.5 + 5·log10(D/10 mm) (so 100 mm → 12.5), nudged up a little by magnification (higher power
// darkens the sky background), capped.
func visualLimitMag(apertureMM, magX float64) float64 {
	if apertureMM <= 0 {
		return 0
	}
	lim := visualLimitBase + 5*math.Log10(apertureMM/10)
	if magX > 0 {
		lim += clamp(0.5*math.Log10(magX/50), -0.5, 1.0)
	}
	return lim
}

// isPointLikeType reports whether a target reads as star-like in the eyepiece (clusters resolve into
// stars), so its integrated magnitude — not its surface brightness — governs visual detectability.
func isPointLikeType(objType string) bool {
	return objType == "cluster" || objType == "globular"
}

// visualDetectabilityScore rates how easily the eye sees a target through the chosen eyepiece. Star-like
// targets (and any with unknown size) are limited by the aperture's point-source magnitude limit;
// extended targets are limited by surface-brightness contrast against the dark sky, eased by aperture.
// Returns a neutral, flagged 0.5 when the magnitude is unknown.
func visualDetectabilityScore(objType string, magV, diamArcmin, apertureMM float64, view EyepieceView, hasMag, hasDiameter bool) (score float64, isKnown bool) {
	if !hasMag || apertureMM <= 0 {
		return 0.5, false
	}
	limit := visualLimitMag(apertureMM, view.MagX)
	if !hasDiameter || diamArcmin <= 0 || isPointLikeType(objType) {
		return clamp01((limit-magV)/visualPointRange + 0.5), true
	}
	area := math.Pi * (diamArcmin / 2) * (diamArcmin / 2)
	sb := surfaceBrightness(magV, area)
	apertureGain := 2.5 * math.Log10(apertureMM/refApertureMM)
	contrast := skyDarkSBArcmin - sb + apertureGain
	return clamp01(contrast/visualContrastRange + 0.5), true
}

// surfaceBrightness returns mag per arcmin²: magV + 2.5·log10(area).
func surfaceBrightness(magV, areaArcmin2 float64) float64 {
	if areaArcmin2 <= 0 {
		return magV
	}
	return magV + 2.5*math.Log10(areaArcmin2)
}

// moonScore is the multiplicative Moon factor in [0,1] (1 = no interference). It is 1 when the Moon
// is below the horizon; otherwise it grows with the Moon's illumination, altitude (sky glow) and
// proximity to the target, scaled by the target's sensitivity to moonlight.
func moonScore(moonUp bool, illum, moonAltDeg, sepDeg, sensitivity float64) float64 {
	if !moonUp {
		return 1.0
	}
	proximity := 1 - clamp01((sepDeg-moonSepMinDeg)/(moonSepSafeDeg-moonSepMinDeg))
	altFactor := clamp01(math.Sin(moonAltDeg * math.Pi / 180))
	glow := illum * altFactor * proximity
	return clamp01(1 - sensitivity*glow)
}

// moonSensitivity estimates how badly moonlight hurts a target: emission nebulae (narrowband-friendly)
// shrug it off; faint broadband targets suffer most.
func moonSensitivity(objType string, sbKnown bool, sb float64) float64 {
	if objType == "emission_nebula" {
		return 0.25
	}
	if !sbKnown {
		return 0.7
	}
	return clamp(0.4+(sb-19)/(23-19)*0.6, 0.4, 1.0)
}

// moonSensitivityVisual is the eyepiece counterpart of moonSensitivity: moonlight hurts the eye more
// than a camera (no narrowband escape; a brighter sky destroys low-contrast detail directly), so the
// floor is higher across the board.
func moonSensitivityVisual(objType string, sbKnown bool, sb float64) float64 {
	if objType == "emission_nebula" {
		return 0.55
	}
	if !sbKnown {
		return 0.85
	}
	return clamp(0.6+(sb-19)/(23-19)*0.4, 0.6, 1.0)
}

// Light-pollution scoring tunables. Like the Moon, artificial sky glow is a multiplicative penalty: it
// crushes contrast on faint/low-surface-brightness targets and barely touches bright ones. glow ramps
// 0→1 as the site brightens from sqmLpPristine (Bortle 1) to sqmLpInner (inner city); lpStrength keeps
// it "balanced" — clearly penalizing yet floored at lpFloor, never a hard zero, so a city site still
// ranks its own best targets.
const (
	sqmLpPristine = 21.8
	sqmLpInner    = 17.8
	lpStrength    = 0.75
	lpFloor       = 0.20
)

// lightPollutionScore is the multiplicative sky-glow factor in [0,1] (1 = pristine, or unknown site).
// It reuses the target's moonlight sensitivity, since the same physics applies: narrowband-friendly
// emission nebulae shrug off light pollution while faint broadband targets suffer most.
func lightPollutionScore(siteSQM, sensitivity float64) float64 {
	if siteSQM <= 0 {
		return 1 // unknown site brightness → no penalty
	}
	glow := clamp01((sqmLpPristine - siteSQM) / (sqmLpPristine - sqmLpInner))
	return clamp(1-sensitivity*glow*lpStrength, lpFloor, 1)
}

// composite combines the weighted base sub-scores with the Moon and light-pollution multipliers into a
// 0–100 score.
func composite(w Weights, s SubScores) int {
	base := w.MaxAlt*s.MaxAlt + w.DarkHours*s.DarkHours + w.Framing*s.Framing +
		w.Detect*s.Detectability + w.AltNow*s.AltNow
	lp := s.LightPollution
	if lp == 0 { // unset (e.g. a zero-value SubScores) → no light-pollution penalty
		lp = 1
	}
	return int(math.Round(clamp01(base*s.Moon*lp) * 100))
}

// buildReason renders a short, human-readable explanation of a visible target's score.
func buildReason(t Target, moonUp bool, illum float64) string {
	parts := []string{
		fmt.Sprintf("Peaks at %.0f°", t.MaxAltDeg),
		fmt.Sprintf("%.1f h in darkness", t.DarkHoursAboveMin),
	}
	if t.ChosenEyepiece != "" {
		parts = append(parts, fmt.Sprintf("%s @ %.0f×, %.1f° field", t.ChosenEyepiece, t.MagX, t.TrueFOVDeg))
	} else if t.Flags.FramingKnown {
		parts = append(parts, fmt.Sprintf("fills %.0f%% of frame", t.FovFillPct))
	}
	if moonUp {
		parts = append(parts, fmt.Sprintf("moon %.0f%% at %.0f°", illum*100, t.MoonSepDeg))
	} else {
		parts = append(parts, "no moon")
	}
	return strings.Join(parts, ", ") + "."
}

func clamp01(x float64) float64 { return clamp(x, 0, 1) }

func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
