package solarsystem

// The satellites.
//
// Earth's Moon is the one moon whose motion a fixed element set describes badly — evection and the
// variation move it by more than a degree, which at Earth–Moon scale is thousands of kilometres and
// visibly wrong during an eclipse. So it is driven by the Astronomical Almanac series the engine
// already uses for moon-glow, rise/set and illumination (internal/astro), named here as a Series and
// mirrored in the browser. Using the same series everywhere is what stops the Moon on this map from
// disagreeing with the Moon on the Tonight page.
//
// The other major satellites are mean-element bodies and arrive with the rest of the systems.

// SeriesMoonAA is the Astronomical Almanac low-precision lunar series: geocentric, ~0.3° in
// direction and a few hundred kilometres in distance.
const SeriesMoonAA = "moon_aa"

func moonTable() []Body {
	return []Body{{
		Key: "moon", Kind: KindMoon, Parent: "earth",
		RadiusKm: 1737.4, MassKg: 7.342e22, Albedo: 0.136, Colour: "#9A968E",
		// Tidally locked: the prime-meridian rate equals the orbital mean motion, which is why the
		// same face is drawn toward Earth at every instant without anything having to enforce it.
		Pole:    Pole{RA0: 269.9949, RADot: 0.0031, Dec0: 66.5392, DecDot: 0.0130, W0: 38.3213, WDot: 13.17635815},
		Series:  SeriesMoonAA,
		Texture: "moon",
		Tier:    TierFitted,
		Source:  srcPhysical + "; " + srcPole + "; motion: Astronomical Almanac low-precision series",
	}}
}
