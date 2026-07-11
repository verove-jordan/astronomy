package align

import (
	"math"
	"strings"

	"github.com/verove-jordan/astronomy/internal/astro"
)

const (
	magBrightRef   = -1.5 // ≈ Sirius — the bright end used to normalize the brightness term
	altPeakDeg     = 50.0 // altitude sweet spot for an alignment star (easy to reach, low refraction)
	altHalfDeg     = 42.0 // half-width of the altitude bump
	minSepFloorDeg = 20.0 // initial minimum separation between chosen stars (relaxed if stars are scarce)
	sepRelaxStep   = 10.0

	// suitability weights (intrinsic per-star quality)
	wBright = 0.6
	wAlt    = 0.4

	// marginal-gain weights (greedy spread selection)
	wSuit = 0.5
	wSep  = 0.4
	wAz   = 0.1
)

// positioned is a catalog star resolved to the request's site and time: precessed RA/Dec plus the
// horizontal coordinates and hour angle used for filtering, scoring and ordering.
type positioned struct {
	Star
	raNow, decNow float64
	alt, az       float64
	ha            float64 // local hour angle, west-positive, (-180,180]
	side          string  // meridian side: "east" | "west"
}

// positionedCatalog resolves every catalog star to alt/az/HA at the request site and time.
func positionedCatalog(p Params) []positioned {
	out := make([]positioned, 0, len(catalog))
	for _, s := range catalog {
		ra, dec := astro.PrecessFromJ2000(s.RADeg, s.DecDeg, p.At)
		alt, az := astro.Horizontal(ra, dec, p.Lat, p.Lon, p.At)
		ha := astro.HourAngleDeg(ra, p.Lon, p.At)
		out = append(out, positioned{
			Star: s, raNow: ra, decNow: dec, alt: alt, az: az, ha: ha, side: meridianSide(ha),
		})
	}
	return out
}

// meridianSide maps an hour angle (west-positive) to the side of the meridian the object sits on:
// negative HA = not yet transited = eastern sky; non-negative = western sky.
func meridianSide(ha float64) string {
	if ha < 0 {
		return "east"
	}
	return "west"
}

// resolveAccepted picks out, in the given order, the catalog stars the user has already centered.
func resolveAccepted(cands []positioned, names []string) []positioned {
	out := make([]positioned, 0, len(names))
	for _, n := range names {
		for i := range cands {
			if strings.EqualFold(cands[i].Name, strings.TrimSpace(n)) {
				out = append(out, cands[i])
				break
			}
		}
	}
	return out
}

// chosenMeridianSide fixes which side of the meridian a same-side profile must use: the side of the
// first accepted star, otherwise the side carrying the stronger pool of in-band candidates.
func chosenMeridianSide(accepted, cands []positioned, profile Profile) string {
	if len(accepted) > 0 {
		return accepted[0].side
	}
	var east, west float64
	for _, c := range cands {
		if !inBand(c, profile) || c.Mag > profile.MagLimit || astro.ApparentAltitude(c.alt) <= 0 {
			continue
		}
		if !inStarList(profile.StarList, c.Name) {
			continue // weigh only stars the hand controller can offer
		}
		if c.side == "east" {
			east += suitability(c, profile)
		} else {
			west += suitability(c, profile)
		}
	}
	if west > east {
		return "west"
	}
	return "east"
}

// eligible returns the candidate pool for the greedy fill: in the altitude band, above the (refracted)
// horizon, within the magnitude limit, not rejected or already accepted, and respecting the profile's
// meridian rules.
func eligible(cands []positioned, profile Profile, side string, rejected map[string]bool, accepted []positioned) []positioned {
	acc := make(map[string]bool, len(accepted))
	for _, a := range accepted {
		acc[strings.ToLower(a.Name)] = true
	}
	out := make([]positioned, 0, len(cands))
	for _, c := range cands {
		key := strings.ToLower(c.Name)
		switch {
		case rejected[key] || acc[key]:
		case !inBand(c, profile):
		case astro.ApparentAltitude(c.alt) <= 0:
		case c.Mag > profile.MagLimit:
		case !inStarList(profile.StarList, c.Name): // not offered by the hand controller
		case side != "any" && c.side != side:
		case profile.AvoidMeridianDeg > 0 && math.Abs(c.ha) < profile.AvoidMeridianDeg:
		default:
			out = append(out, c)
		}
	}
	return out
}

func inBand(c positioned, profile Profile) bool {
	return c.alt >= profile.MinAltDeg && c.alt <= profile.MaxAltDeg
}

// greedyFill adds stars one at a time, each maximizing the marginal spread gain over those already
// chosen while honoring a minimum-separation floor. The floor relaxes (20→10→0°) only when no
// candidate clears it, so a cramped or obstructed sky still yields a plan instead of nothing. It
// returns the extended slice and how many stars it actually added.
func greedyFill(chosen, pool []positioned, profile Profile, need int) ([]positioned, int) {
	out := chosen
	added := 0
	sepFloor := minSepFloorDeg
	for added < need {
		best := -1
		bestScore := math.Inf(-1)
		for i := range pool {
			if containsName(out, pool[i].Name) {
				continue
			}
			if len(out) > 0 && minSep(pool[i], out) < sepFloor {
				continue
			}
			if sc := marginal(pool[i], out, profile); sc > bestScore {
				bestScore = sc
				best = i
			}
		}
		if best < 0 {
			if sepFloor > 0 {
				sepFloor = math.Max(0, sepFloor-sepRelaxStep)
				continue
			}
			break // pool exhausted
		}
		out = append(out, pool[best])
		added++
	}
	return out, added
}

// marginal scores how much a candidate improves the current set: its intrinsic suitability, its
// distance from the nearest already-chosen star (farthest-point spread), a bonus for opening a new
// azimuth quadrant, minus an alt-az penalty for sitting near the zenith.
func marginal(c positioned, chosen []positioned, profile Profile) float64 {
	sep := 1.0
	if len(chosen) > 0 {
		sep = clamp01(minSep(c, chosen) / 90.0)
	}
	score := wSuit*suitability(c, profile) + wSep*sep + wAz*azQuadrantBonus(c, chosen)
	return score - profile.ZenithBias*zenithCloseness(c.alt)
}

// suitability is the intrinsic [0,1] quality of a star: bright and at a comfortable altitude.
func suitability(c positioned, profile Profile) float64 {
	return clamp01(wBright*brightTerm(c.Mag, profile.MagLimit) + wAlt*altTerm(c.alt))
}

func brightTerm(mag, magLimit float64) float64 {
	span := magLimit - magBrightRef
	if span <= 0 {
		return 0
	}
	return clamp01((magLimit - mag) / span)
}

func altTerm(alt float64) float64 {
	d := (alt - altPeakDeg) / altHalfDeg
	return clamp01(1 - d*d)
}

// minSep is the smallest great-circle separation (degrees) from c to any star in others.
func minSep(c positioned, others []positioned) float64 {
	m := math.Inf(1)
	for _, o := range others {
		if s := astro.AngularSeparation(c.raNow, c.decNow, o.raNow, o.decNow); s < m {
			m = s
		}
	}
	if math.IsInf(m, 1) {
		return 180
	}
	return m
}

func azQuadrantBonus(c positioned, chosen []positioned) float64 {
	q := quadrant(c.az)
	for _, o := range chosen {
		if quadrant(o.az) == q {
			return 0
		}
	}
	return 1
}

func quadrant(az float64) int {
	return int(math.Mod(az, 360)/90) % 4
}

func zenithCloseness(alt float64) float64 {
	return clamp01((alt - 60) / 30)
}

// qualityScore rates the geometry of the chosen set 0–100: mean pairwise separation, azimuth
// coverage and altitude spread. It drives the UI badge and makes the cost of skipping visible.
func qualityScore(chosen []positioned) float64 {
	n := len(chosen)
	if n < 2 {
		return float64(n * 10)
	}
	var sumSep, pairs float64
	minA, maxA := 90.0, 0.0
	quads := map[int]bool{}
	for i := 0; i < n; i++ {
		minA = math.Min(minA, chosen[i].alt)
		maxA = math.Max(maxA, chosen[i].alt)
		quads[quadrant(chosen[i].az)] = true
		for j := i + 1; j < n; j++ {
			sumSep += astro.AngularSeparation(chosen[i].raNow, chosen[i].decNow, chosen[j].raNow, chosen[j].decNow)
			pairs++
		}
	}
	sepN := clamp01((sumSep / pairs) / 90.0)
	azN := float64(len(quads)) / math.Min(float64(n), 4)
	altN := clamp01((maxA - minA) / 40.0)
	return round(100*(0.5*sepN+0.3*azN+0.2*altN), 1)
}

// compassPoint names an azimuth on the 16-point compass (N, NNE, … NNW).
func compassPoint(az float64) string {
	dirs := []string{"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE", "S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW"}
	i := int(math.Mod(az/22.5+0.5, 16))
	if i < 0 {
		i += 16
	}
	return dirs[i]
}

func containsName(list []positioned, name string) bool {
	for _, p := range list {
		if strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}

func toLowerSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		if t := strings.TrimSpace(n); t != "" {
			m[strings.ToLower(t)] = true
		}
	}
	return m
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
