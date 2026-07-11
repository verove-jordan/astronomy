package align

import "github.com/verove-jordan/astronomy/internal/astro"

// SkyBody is the Moon or a naked-eye planet, resolved to the request site/time, returned with the plan so
// the sky map can draw it as a landmark ("the target is just left of Jupiter"). Only bodies above the
// horizon are returned.
type SkyBody struct {
	Name   string  `json:"name"` // lowercase key: "moon" | "venus" | "mars" | … (matches frontend i18n)
	Kind   string  `json:"kind"` // "moon" | "planet"
	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	AltDeg float64 `json:"alt_deg"`
	AzDeg  float64 `json:"az_deg"`
	Mag    float64 `json:"mag"`
	Phase  float64 `json:"phase,omitempty"` // Moon illuminated fraction 0..1 (0 for planets)
}

// nakedEyePlanets are the planets worth showing as landmarks (Uranus/Neptune are not naked-eye).
var nakedEyePlanets = []astro.Planet{astro.Mercury, astro.Venus, astro.Mars, astro.Jupiter, astro.Saturn}

// skyBodies returns the Moon + naked-eye planets that are currently above the (refracted) horizon at the
// request site and time.
func skyBodies(p Params) []SkyBody {
	var out []SkyBody

	m := astro.MoonNow(p.At, p.Lat, p.Lon)
	if astro.ApparentAltitude(m.AltDeg) > 0 {
		out = append(out, SkyBody{
			Name: "moon", Kind: "moon",
			RADeg: round(m.RADeg, 4), DecDeg: round(m.DecDeg, 4),
			AltDeg: round(m.AltDeg, 2), AzDeg: round(m.AzDeg, 2),
			Mag: -12.7, Phase: round(m.IllumFraction, 3),
		})
	}

	for _, pl := range nakedEyePlanets {
		st := astro.PlanetPosition(pl, p.At)
		alt, az := astro.Horizontal(st.RADeg, st.DecDeg, p.Lat, p.Lon, p.At)
		if astro.ApparentAltitude(alt) <= 0 {
			continue
		}
		out = append(out, SkyBody{
			Name: pl.String(), Kind: "planet",
			RADeg: round(st.RADeg, 4), DecDeg: round(st.DecDeg, 4),
			AltDeg: round(alt, 2), AzDeg: round(az, 2),
			Mag: round(st.Magnitude, 1),
		})
	}
	return out
}
