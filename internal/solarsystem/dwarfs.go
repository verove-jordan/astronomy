package solarsystem

import "github.com/verove-jordan/astronomy/internal/astro"

// The dwarf planets.
//
// Pluto is here rather than among the planets because that is what it is, but its orbit comes from
// the same Standish table the planets do — the table includes it, fitted over the same 1800–2050
// span — so it is a fitted body, not a mean-element one.
//
// Ceres, Eris, Haumea and Makemake arrive with the far field.

func dwarfTable() []Body {
	return []Body{{
		Key: "pluto", Kind: KindDwarf,
		RadiusKm: 1188.3, MassKg: 1.303e22, Albedo: 0.52, Colour: "#CBB8A2",
		// Pluto is tipped past a right angle: its rotation is retrograde, and Charon orbits in the
		// same plane, so the pair presents its pole to the Sun for a quarter of every 248-year year.
		Pole:    Pole{RA0: 132.993, Dec0: -6.163, W0: 302.695, WDot: 56.3625225},
		Orbit:   specFromStandish(astro.BodyPluto),
		Texture: "pluto",
		Tier:    TierFitted,
		Source:  srcPhysical + "; " + srcPole,
	}}
}
