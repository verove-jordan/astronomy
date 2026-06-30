package skyevents

import (
	"fmt"
	"math"
	"strings"
)

// Brightness limits per instrument (apparent visual magnitude). Telescope is derived from aperture.
const (
	nakedEyeLimit  = 6.0
	binocularLimit = 9.5
)

// telescopeLimit estimates a faintest-visible magnitude for an aperture (mm): ~7.5 + 5·log10(D/10mm).
// A 100 mm scope ≈ 12.5. Falls back to the binocular limit when aperture is unknown.
func telescopeLimit(apertureMM float64) float64 {
	if apertureMM <= 0 {
		return binocularLimit
	}
	return math.Max(binocularLimit, 7.5+5*math.Log10(apertureMM/10.0))
}

// altFactor maps an object's altitude (deg) at its best dark moment to an observability weight 0..1.
func altFactor(altDeg float64) float64 {
	if altDeg <= 3 {
		return 0
	}
	return clampf(altDeg/50.0, 0, 1)
}

// sunUpFactor weights a daytime/solar event by the Sun's altitude (only that the Sun is up matters).
func sunUpFactor(altDeg float64) float64 {
	if altDeg <= 0 {
		return 0
	}
	return clampf(0.4+altDeg/30.0, 0.4, 1)
}

// observability is the 0..1 site weight applied to every tier score for an event. Faint events are
// dimmed by both moonlight and the site's light pollution; bright/timed events ignore the sky glow.
func observability(e *Event, siteSQM float64) float64 {
	switch e.Kind {
	case "equinox", "solstice", "perihelion", "aphelion":
		return 1 // calendar instants — always "happen"
	case "solar_eclipse":
		return sunUpFactor(e.AltAtBestDeg)
	case "satellite_transit":
		if e.Subtype == "sun" {
			return sunUpFactor(e.AltAtBestDeg)
		}
		return altFactor(e.AltAtBestDeg)
	case "lunar_eclipse", "moon_phase", "supermoon":
		return altFactor(e.AltAtBestDeg)
	default:
		return altFactor(e.AltAtBestDeg) * moonFactorValue(e) * lightPollutionFactorValue(e, siteSQM)
	}
}

// moonFactorValue is the 0..1 moonlight penalty for faint targets (1 = no interference).
func moonFactorValue(e *Event) float64 {
	if !isFaint(e) {
		return 1
	}
	prox := 1.0 - clampf(e.MoonSepDeg/90.0, 0, 1)
	return clampf(1.0-e.MoonIllum*prox*0.8, 0.2, 1)
}

// Light-pollution tunables for the calendar: artificial sky glow dims faint targets just like the Moon.
// Bright naked-eye events shrug it off (isFaint=false → 1). Mirrors skyplan's balanced model.
const (
	lpEvPristineSQM = 21.8
	lpEvInnerSQM    = 17.8
	lpEvStrength    = 0.7
	lpEvFloor       = 0.25
)

// lightPollutionFactorValue is the 0..1 sky-glow penalty from artificial light at the site, applied to
// faint events (1 = pristine site, unknown brightness, or a bright event).
func lightPollutionFactorValue(e *Event, siteSQM float64) float64 {
	if siteSQM <= 0 || !isFaint(e) {
		return 1
	}
	glow := clampf((lpEvPristineSQM-siteSQM)/(lpEvPristineSQM-lpEvInnerSQM), 0, 1)
	return clampf(1-glow*lpEvStrength, lpEvFloor, 1)
}

// MagLimits returns the faintest visible magnitude per instrument tier for the given aperture (mm).
func MagLimits(apertureMM float64) (nakedEye, binocular, telescope float64) {
	return nakedEyeLimit, binocularLimit, telescopeLimit(apertureMM)
}

// isFaint marks events whose visibility is sensitive to moonlight/altitude (comets, showers, dim planets).
func isFaint(e *Event) bool {
	return e.Kind == "comet" || e.Kind == "meteor_shower" || (e.HasMag && e.Magnitude > 3)
}

// tierCaps returns the per-instrument capability (0..1) before applying observability. Magnitude-bearing
// events gate on each tier's limiting magnitude; the rest use per-kind defaults.
func tierCaps(e *Event, apertureMM float64) [3]float64 {
	if e.HasMag {
		return [3]float64{
			magCap(e.Magnitude, nakedEyeLimit),
			magCap(e.Magnitude, binocularLimit),
			magCap(e.Magnitude, telescopeLimit(apertureMM)),
		}
	}
	switch e.Kind {
	case "solar_eclipse":
		if e.Subtype == "partial" {
			return [3]float64{0.8, 0.9, 1}
		}
		return [3]float64{1, 1, 1}
	case "lunar_eclipse":
		return [3]float64{1, 0.9, 0.8}
	case "meteor_shower":
		return [3]float64{1, 0.4, 0.15}
	case "moon_phase", "supermoon":
		return [3]float64{1, 0.85, 0.7}
	case "satellite_transit":
		return [3]float64{0.25, 0.7, 1.0} // a capture target — telescope/camera best
	case "equinox", "solstice":
		return [3]float64{0.5, 0.2, 0.2}
	case "perihelion", "aphelion":
		return [3]float64{0.35, 0.15, 0.15}
	}
	return [3]float64{0.8, 0.9, 1}
}

// magCap returns how well an instrument of the given limiting magnitude shows an object of magnitude m
// (0 if beyond the limit, ramping to 1 with brightness headroom).
func magCap(m, limit float64) float64 {
	if m > limit {
		return 0
	}
	return clampf((limit-m)/4.0+0.5, 0.25, 1)
}

// rarityCeiling is the spectacle/rarity ceiling (0..100) an event can reach — the headline ranking
// driver. Satellite Sun/Moon transits, total eclipses and bright comets top the list.
func rarityCeiling(e *Event) int {
	switch e.Kind {
	case "satellite_transit":
		if e.Subtype == "sun" {
			return 96
		}
		return 94
	case "solar_eclipse":
		switch e.Subtype {
		case "total":
			return 99
		case "annular", "annular_total":
			return 96
		default:
			return 80
		}
	case "lunar_eclipse":
		switch e.Subtype {
		case "total":
			return 92
		case "partial":
			return 75
		default:
			return 55
		}
	case "comet":
		return int(clampf(95-e.Magnitude*5, 35, 96))
	case "opposition":
		return 78
	case "elongation":
		return 66
	case "conjunction":
		return int(clampf(60+(3-clampf(e.SeparationDeg, 0, 3))/3*25, 50, 88))
	case "planet_moon":
		return int(clampf(58+(4-clampf(e.SeparationDeg, 0, 4))/4*18, 52, 78))
	case "meteor_shower":
		return int(clampf(50+e.ZHR/120*40, 45, 92))
	case "supermoon":
		return 62
	case "moon_phase":
		switch e.Subtype {
		case "full":
			return 46
		case "new":
			return 40
		default:
			return 30
		}
	case "equinox", "solstice":
		return 36
	case "perihelion", "aphelion":
		return 24
	}
	return 40
}

// scoreEvent fills Visibility (per instrument), the overall ranking Score, and the explanatory factors.
func scoreEvent(e *Event, apertureMM, siteSQM float64) {
	o := observability(e, siteSQM)
	caps := tierCaps(e, apertureMM)
	mk := func(c float64) int { return int(math.Round(100 * o * c)) }
	e.Visibility = Visibility{NakedEye: mk(caps[0]), Binocular: mk(caps[1]), Telescope: mk(caps[2])}
	best := maxInt(e.Visibility.NakedEye, e.Visibility.Binocular, e.Visibility.Telescope)
	e.Instrument = bestTierName(e.Visibility)
	e.Score = int(clampf(float64(rarityCeiling(e))*float64(best)/100.0, 0, 100))
	e.ScoreFactors = explainScore(e, apertureMM, siteSQM)
}

// explainScore decomposes the score into the human-readable factors that produced it: how rare the
// event is, how well placed it is from the site (altitude / Sun-up / Moon), and how bright it is for
// the best instrument. The UI renders these as labelled bars + a summary; Detail carries the raw value.
func explainScore(e *Event, apertureMM, siteSQM float64) []ScoreFactor {
	f := []ScoreFactor{
		{Key: "rarity", Weight: round2(clampf(float64(rarityCeiling(e))/100, 0, 1))},
	}
	switch e.Kind {
	case "solar_eclipse":
		f = append(f, ScoreFactor{Key: "sun_up", Weight: round2(sunUpFactor(e.AltAtBestDeg)), Detail: degStr(e.AltAtBestDeg)})
	case "satellite_transit":
		if e.Subtype == "sun" {
			f = append(f, ScoreFactor{Key: "sun_up", Weight: round2(sunUpFactor(e.AltAtBestDeg)), Detail: degStr(e.AltAtBestDeg)})
		} else {
			f = append(f, ScoreFactor{Key: "altitude", Weight: round2(altFactor(e.AltAtBestDeg)), Detail: degStr(e.AltAtBestDeg)})
		}
	case "equinox", "solstice", "perihelion", "aphelion":
		// a calendar instant — no site dependence
	default:
		f = append(f, ScoreFactor{Key: "altitude", Weight: round2(altFactor(e.AltAtBestDeg)), Detail: degStr(e.AltAtBestDeg)})
		if isFaint(e) {
			f = append(f, ScoreFactor{
				Key:    "moon",
				Weight: round2(moonFactorValue(e)),
				Detail: fmt.Sprintf("%.0f%% · %.0f°", e.MoonIllum*100, e.MoonSepDeg),
			})
			if siteSQM > 0 {
				f = append(f, ScoreFactor{
					Key:    "light_pollution",
					Weight: round2(lightPollutionFactorValue(e, siteSQM)),
					Detail: fmt.Sprintf("%.1f mag/arcsec²", siteSQM),
				})
			}
		}
	}

	caps := tierCaps(e, apertureMM)
	best := caps[0]
	for _, c := range caps[1:] {
		if c > best {
			best = c
		}
	}
	bf := ScoreFactor{Key: "brightness", Weight: round2(best)}
	if e.HasMag {
		bf.Detail = fmt.Sprintf("mag %.1f", e.Magnitude)
	}
	return append(f, bf)
}

func degStr(altDeg float64) string { return fmt.Sprintf("%.0f°", altDeg) }

// bestTierName labels the least equipment that shows the event (naked-eye is the headline when possible).
func bestTierName(v Visibility) string {
	switch {
	case v.NakedEye > 0:
		return "naked_eye"
	case v.Binocular > 0:
		return "binocular"
	case v.Telescope > 0:
		return "telescope"
	}
	return "none"
}

// finalizeEvent scores, ids and rounds an event after generation.
func finalizeEvent(e *Event, prm Params) {
	scoreEvent(e, prm.Optics.ApertureMM, prm.SiteSQM)
	if e.ID == "" {
		e.ID = fmt.Sprintf("%s_%d_%s", e.Kind, dayKey(e.PeakUTCMs), strings.Join(e.Bodies, "-"))
	}
	if e.Title == "" {
		e.Title = titleCase(e.Kind)
	}
	e.AltAtBestDeg = round1(e.AltAtBestDeg)
	e.MoonSepDeg = round1(e.MoonSepDeg)
	e.MoonIllum = round2(e.MoonIllum)
	if e.SeparationDeg != 0 {
		e.SeparationDeg = round2(e.SeparationDeg)
	}
	if e.HasMag {
		e.Magnitude = round1(e.Magnitude)
	}
}
