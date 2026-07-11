package align

import (
	"fmt"
	"time"
)

// Params is the observer context a plan is computed for.
type Params struct {
	At  time.Time
	Lat float64
	Lon float64
}

// AlignStar is one recommended (or accepted) calibration star, resolved to the request site and time.
type AlignStar struct {
	Name          string   `json:"name"`
	Constellation string   `json:"constellation"`
	RADeg         float64  `json:"ra_deg"`  // precessed to the epoch of the request
	DecDeg        float64  `json:"dec_deg"` //
	Mag           float64  `json:"mag"`
	AltDeg        float64  `json:"alt_deg"`
	AzDeg         float64  `json:"az_deg"`
	Compass       string   `json:"compass"`           // 16-point direction of the azimuth (e.g. "SE")
	HourAngleDeg  float64  `json:"hour_angle_deg"`    // west-positive
	MeridianSide  string   `json:"meridian_side"`     // "east" | "west"
	Order         int      `json:"order"`             // 1-based position in the alignment sequence
	Status        string   `json:"status"`            // "accepted" | "recommended" | "upcoming"
	Phase         string   `json:"phase,omitempty"`   // "align" | "calibration"; empty on single-phase profiles
	HCName        string   `json:"hc_name,omitempty"` // the exact hand-controller label (profiles with a StarList)
	Suitability   float64  `json:"suitability"`       // intrinsic per-star quality [0,1]
	Reasons       []string `json:"reasons"`           // short human-readable picks ("bright", "well placed", …)
}

// Result is the full ordered alignment plan: the locked accepted stars first, then the greedily
// chosen recommendations, plus a geometry quality score and any soft warnings.
type Result struct {
	Hemisphere   string      `json:"hemisphere"`    // "north" | "south"
	Profile      string      `json:"profile"`       // echoed profile key
	MountType    string      `json:"mount_type"`    // "eq" | "altaz"
	MeridianSide string      `json:"meridian_side"` // "east" | "west" | "any"
	Stars        []AlignStar `json:"stars"`
	QualityScore float64     `json:"quality_score"` // 0–100 geometry quality of the chosen set
	Warnings     []string    `json:"warnings"`
	// SkyBodies are the Moon + naked-eye planets currently above the horizon, for the sky map's landmarks
	// (they can't live in the static star catalogue because they move).
	SkyBodies []SkyBody `json:"sky_bodies,omitempty"`
}

// Plan returns an ordered set of bright alignment stars for the site/time, mount profile and star
// count. accepted stars are locked first (and constrain the rest); rejected stars are excluded and
// replaced. It is a pure function of its inputs — the caller (HTTP handler / store) holds the
// accepted/rejected sets and re-plans on every skip or accept.
//
// Two-phase profiles (AlignStars > 0, e.g. Celestron EQ) split the count into alignment stars
// followed by calibration stars picked across the meridian (see phases.go). Accepted names fill the
// alignment slots first, then calibration — this assumes the caller accepts stars in plan order,
// which the UI's "centered — next" flow guarantees; MeridianSide stays the alignment-phase side.
func Plan(p Params, profile Profile, count int, accepted, rejected []string) Result {
	count = profile.ClampCount(count)
	north := p.Lat >= 0

	res := Result{
		Hemisphere:   hemisphere(north),
		Profile:      profile.Key,
		MountType:    profile.MountType,
		MeridianSide: "any",
	}

	cands := positionedCatalog(p)
	acceptedStars := resolveAccepted(cands, accepted)

	side := "any"
	if profile.SameMeridianSide {
		side = chosenMeridianSide(acceptedStars, cands, profile)
		res.MeridianSide = side
	}

	alignN, calibN := phaseSplit(profile, count)
	rej := toLowerSet(rejected)
	pool := eligible(cands, profile, side, rej, acceptedStars)

	// Phase 1 — alignment stars (single-phase profiles: the whole plan).
	chosen := append([]positioned(nil), acceptedStars...)
	if len(chosen) < alignN {
		chosen, _ = greedyFill(chosen, pool, profile, alignN-len(chosen))
	}

	// Phase 2 — calibration stars; accepted names beyond the alignment slots already fill some.
	var calibWarnings []string
	if calibN > 0 {
		calibHave := len(chosen) - alignN
		chosen, calibWarnings = fillCalibration(chosen, cands, profile, side, rej, acceptedStars, calibN-calibHave)
	}

	calibStart := -1 // single-phase: no phase labels
	if profile.AlignStars > 0 {
		calibStart = alignN
	}
	res.Stars = buildStars(chosen, len(acceptedStars), profile, calibStart)
	res.QualityScore = qualityScore(chosen)
	res.Warnings = append(planWarnings(len(chosen), count, profile, side), calibWarnings...)

	// Sky-map landmarks: the Moon + naked-eye planets currently up (context for finding the target by eye).
	res.SkyBodies = skyBodies(p)
	return res
}

// buildStars assembles the ordered AlignStar list, tagging the first numAccepted as accepted, the next
// as the recommended star to center now, and the remainder as upcoming previews. calibStart is the
// index where the calibration phase begins (two-phase profiles); -1 leaves every star phase-less.
func buildStars(chosen []positioned, numAccepted int, profile Profile, calibStart int) []AlignStar {
	out := make([]AlignStar, len(chosen))
	for i, c := range chosen {
		status := "upcoming"
		switch {
		case i < numAccepted:
			status = "accepted"
		case i == numAccepted:
			status = "recommended"
		}
		phase := ""
		if calibStart >= 0 {
			phase = "align"
			if i >= calibStart {
				phase = "calibration"
			}
		}
		out[i] = AlignStar{
			Name:          c.Name,
			Constellation: c.Constellation,
			RADeg:         round(c.raNow, 4),
			DecDeg:        round(c.decNow, 4),
			Mag:           c.Mag,
			AltDeg:        round(c.alt, 2),
			AzDeg:         round(c.az, 2),
			Compass:       compassPoint(c.az),
			HourAngleDeg:  round(c.ha, 2),
			MeridianSide:  c.side,
			Order:         i + 1,
			Status:        status,
			Phase:         phase,
			HCName:        hcLabel(profile.StarList, c.Name),
			Suitability:   round(suitability(c, profile), 3),
			Reasons:       reasonsFor(c, chosen[:i]),
		}
	}
	return out
}

// reasonsFor builds a short, human-readable justification for picking a star: how bright it is, how
// well placed, and (after the first) how much it widens the spread.
func reasonsFor(c positioned, prior []positioned) []string {
	var rs []string
	switch {
	case c.Mag <= 1.0:
		rs = append(rs, fmt.Sprintf("very bright (mag %.1f) — easy to find", c.Mag))
	case c.Mag <= 2.0:
		rs = append(rs, fmt.Sprintf("bright (mag %.1f)", c.Mag))
	default:
		rs = append(rs, fmt.Sprintf("naked-eye (mag %.1f)", c.Mag))
	}
	switch {
	case c.alt < 30:
		rs = append(rs, fmt.Sprintf("fairly low (alt %.0f°)", c.alt))
	case c.alt > 65:
		rs = append(rs, fmt.Sprintf("high overhead (alt %.0f°)", c.alt))
	default:
		rs = append(rs, fmt.Sprintf("well placed (alt %.0f°)", c.alt))
	}
	if len(prior) > 0 {
		rs = append(rs, fmt.Sprintf("%.0f° from the nearest chosen star — keeps the spread wide", minSep(c, prior)))
	}
	return rs
}

// planWarnings flags when the sky could not supply the requested number of stars (obstruction, early
// twilight, a restrictive meridian rule) — the plan still returns whatever it could place.
func planWarnings(have, want int, profile Profile, side string) []string {
	if have >= want {
		return nil
	}
	sideTxt := ""
	if side != "any" {
		sideTxt = fmt.Sprintf(" on the %s side of the meridian", side)
	}
	return []string{fmt.Sprintf(
		"Only %d of %d requested stars are available between %.0f° and %.0f° altitude%s right now. "+
			"Lower the star count, wait for more stars to rise, or try a different mount profile.",
		have, want, profile.MinAltDeg, profile.MaxAltDeg, sideTxt)}
}

func hemisphere(north bool) string {
	if north {
		return "north"
	}
	return "south"
}
