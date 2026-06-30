// Package skyevents computes a calendar of rare, watchable astronomical events for an observing site —
// eclipses, planetary conjunctions/oppositions/elongations, comets, meteor showers, moon phases,
// equinoxes/solstices and satellite (ISS) transits of the Sun/Moon — each scored for visibility with
// the naked eye and with the user's gear. It computes everything locally (Meeus via soniakeys/meeus +
// hand-rolled planet/comet/satellite math), reusing internal/astro for site altitude and internal/
// skyplan for the per-event night chart context. Online feeds (comet elements, satellite TLEs) are
// optional and soft-fail offline.
package skyevents

import "math"

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }

func sinD(deg float64) float64 { return math.Sin(deg * math.Pi / 180) }
func cosD(deg float64) float64 { return math.Cos(deg * math.Pi / 180) }

func acosDeg(x float64) float64 { return math.Acos(clampf(x, -1, 1)) * 180 / math.Pi }

// norm360 wraps an angle to [0, 360).
func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

// angularSepAzEl returns the angular separation (deg) between two horizontal positions.
func angularSepAzEl(az1, el1, az2, el2 float64) float64 {
	return acosDeg(sinD(el1)*sinD(el2) + cosD(el1)*cosD(el2)*cosD(az1-az2))
}

func clampf(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func maxInt(vals ...int) int {
	m := 0
	for i, v := range vals {
		if i == 0 || v > m {
			m = v
		}
	}
	return m
}

// titleCase upper-cases the first letter and turns underscores into spaces ("annular_total" → "Annular
// total"). Used only for English fallback titles; the UI composes localized titles from structured fields.
func titleCase(s string) string {
	out := []rune{}
	upNext := true
	for _, r := range s {
		if r == '_' {
			out = append(out, ' ')
			continue
		}
		if upNext && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		out = append(out, r)
		upNext = false
	}
	return string(out)
}
