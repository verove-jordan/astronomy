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
	Compass       string   `json:"compass"`        // 16-point direction of the azimuth (e.g. "SE")
	HourAngleDeg  float64  `json:"hour_angle_deg"` // west-positive
	MeridianSide  string   `json:"meridian_side"`  // "east" | "west"
	Order         int      `json:"order"`          // 1-based position in the alignment sequence
	Status        string   `json:"status"`         // "accepted" | "recommended" | "upcoming"
	Suitability   float64  `json:"suitability"`    // intrinsic per-star quality [0,1]
	Reasons       []string `json:"reasons"`        // short human-readable picks ("bright", "well placed", …)
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
}

// Plan returns an ordered set of bright alignment stars for the site/time, mount profile and star
// count. accepted stars are locked first (and constrain the rest); rejected stars are excluded and
// replaced. It is a pure function of its inputs — the caller (HTTP handler / store) holds the
// accepted/rejected sets and re-plans on every skip or accept.
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

	pool := eligible(cands, profile, side, toLowerSet(rejected), acceptedStars)

	chosen := append([]positioned(nil), acceptedStars...)
	chosen, _ = greedyFill(chosen, pool, profile, count-len(chosen))

	res.Stars = buildStars(chosen, len(acceptedStars), profile)
	res.QualityScore = qualityScore(chosen)
	res.Warnings = planWarnings(len(chosen), count, profile, side)
	return res
}

// buildStars assembles the ordered AlignStar list, tagging the first numAccepted as accepted, the next
// as the recommended star to center now, and the remainder as upcoming previews.
func buildStars(chosen []positioned, numAccepted int, profile Profile) []AlignStar {
	out := make([]AlignStar, len(chosen))
	for i, c := range chosen {
		status := "upcoming"
		switch {
		case i < numAccepted:
			status = "accepted"
		case i == numAccepted:
			status = "recommended"
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
