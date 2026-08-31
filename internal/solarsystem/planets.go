package solarsystem

import "github.com/verove-jordan/astronomy/internal/astro"

// The Sun and the eight planets.
//
// Radii, masses and albedos are the published measurements (IAU 2015 nominal values / JPL planetary
// fact sheets). The rotational elements are the IAU/IAG WGCCRE report — the mean terms only: the
// small nutation/libration corrections some bodies carry (Neptune's N term, the Moon's E terms)
// move a pole by well under a degree, which is far below anything visible in a map of the system.
// The orbits are Standish's approximate elements, converted to the one per-day form this package
// propagates everything with.

const srcPhysical = "IAU 2015 nominal values / JPL planetary fact sheets"
const srcPole = "IAU WGCCRE 2015 rotational elements (mean terms)"

// julianCentury is the number of days Standish's per-century rates are quoted over.
const julianCentury = 36525.0

// specFromStandish converts one row of the approximate-element table into a Spec. The conversion is
// exact rather than approximate: mean anomaly M = L − ϖ and argument of periapsis ω = ϖ − Ω hold at
// every instant because all three angles are linear in time, so the per-day form reproduces
// astro.HelioEclipticJ2000 to the last bit (pinned by TestSpecFromStandish_MatchesAstro).
func specFromStandish(b astro.Body) *Spec {
	s := astro.ElementTable(b)
	n := (s.LDot - s.PeriDot) / julianCentury
	return &Spec{
		Centre:     "sun",
		Frame:      FrameEcliptic,
		EpochJD:    astro.J2000,
		A:          s.A,
		ADot:       s.ADot / julianCentury,
		E:          s.E,
		EDot:       s.EDot / julianCentury,
		I:          s.IDeg,
		IDot:       s.IDot / julianCentury,
		Node:       s.NodeDeg,
		NDot:       s.NodeDot / julianCentury,
		Peri:       s.PeriDeg - s.NodeDeg,
		PDot:       (s.PeriDot - s.NodeDot) / julianCentury,
		M:          s.LDeg - s.PeriDeg,
		N:          n,
		PeriodDays: 360 / (s.LDot / julianCentury),
	}
}

func sunAndPlanets() []Body {
	return []Body{{
		Key: "sun", Kind: KindStar,
		RadiusKm: 695700, MassKg: 1.98892e30, Colour: "#FFF3D6",
		Pole:    Pole{RA0: 286.13, Dec0: 63.87, W0: 84.176, WDot: 14.1844000},
		Texture: "sun", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "mercury", Kind: KindPlanet,
		RadiusKm: 2439.7, MassKg: 3.3011e23, Albedo: 0.142, Colour: "#8C8680",
		Pole:    Pole{RA0: 281.0103, RADot: -0.0328, Dec0: 61.4155, DecDot: -0.0049, W0: 329.5988, WDot: 6.1385108},
		Orbit:   specFromStandish(astro.BodyMercury),
		Texture: "mercury", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "venus", Kind: KindPlanet,
		RadiusKm: 6051.8, MassKg: 4.8675e24, Albedo: 0.689, Colour: "#E6C58C",
		// Venus turns backwards: WDot is negative, and a Venusian day is longer than its year.
		Pole:    Pole{RA0: 272.76, Dec0: 67.16, W0: 160.20, WDot: -1.4813688},
		Orbit:   specFromStandish(astro.BodyVenus),
		Texture: "venus", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "earth", Kind: KindPlanet,
		RadiusKm: 6378.137, PolarRadiusKm: 6356.752, MassKg: 5.97237e24, Albedo: 0.306, Colour: "#4B7BB5",
		Pole:    Pole{RA0: 0.0, RADot: -0.641, Dec0: 90.0, DecDot: -0.557, W0: 190.147, WDot: 360.9856235},
		Orbit:   specFromStandish(astro.BodyEarth),
		Texture: "earth", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "mars", Kind: KindPlanet,
		RadiusKm: 3396.19, PolarRadiusKm: 3376.20, MassKg: 6.4171e23, Albedo: 0.170, Colour: "#C1502E",
		Pole:    Pole{RA0: 317.68143, RADot: -0.1061, Dec0: 52.88650, DecDot: -0.0609, W0: 176.630, WDot: 350.89198226},
		Orbit:   specFromStandish(astro.BodyMars),
		Texture: "mars", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "jupiter", Kind: KindPlanet,
		RadiusKm: 71492, PolarRadiusKm: 66854, MassKg: 1.8982e27, Albedo: 0.538, Colour: "#C9A87C",
		// W is System III, the rotation of the magnetic field — the one a planetary imager's
		// central-meridian-transit tables are quoted in.
		Pole:  Pole{RA0: 268.056595, RADot: -0.006499, Dec0: 64.495303, DecDot: 0.002413, W0: 284.95, WDot: 870.5360000},
		Orbit: specFromStandish(astro.BodyJupiter),
		Ring: &Ring{InnerKm: 122500, OuterKm: 129000, Faint: true,
			Source: "Jupiter main ring, Ockert-Bell et al. 1999"},
		Texture: "jupiter", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "saturn", Kind: KindPlanet,
		RadiusKm: 60268, PolarRadiusKm: 54364, MassKg: 5.6834e26, Albedo: 0.499, Colour: "#E0CFA0",
		Pole:  Pole{RA0: 40.589, RADot: -0.036, Dec0: 83.537, DecDot: -0.004, W0: 38.90, WDot: 810.7939024},
		Orbit: specFromStandish(astro.BodySaturn),
		// C ring inner edge out to the A ring's outer edge. The Cassini division and the individual
		// ringlets are in the alpha map, not in the geometry.
		Ring: &Ring{InnerKm: 74500, OuterKm: 136780, Texture: "saturn_ring",
			Source: "ring radii, NASA/JPL Cassini"},
		Texture: "saturn", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "uranus", Kind: KindPlanet,
		RadiusKm: 25559, PolarRadiusKm: 24973, MassKg: 8.6810e25, Albedo: 0.488, Colour: "#A8DCE0",
		// A pole at declination −15° is what "rolling on its side" means: the axis lies almost in the
		// orbit plane, and the rotation is retrograde with it.
		Pole:  Pole{RA0: 257.311, Dec0: -15.175, W0: 203.81, WDot: -501.1600928},
		Orbit: specFromStandish(astro.BodyUranus),
		Ring: &Ring{InnerKm: 41800, OuterKm: 51150, Faint: true,
			Source: "ε ring and inner system, Elliot et al. 1978"},
		Texture: "uranus", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}, {
		Key: "neptune", Kind: KindPlanet,
		RadiusKm: 24764, PolarRadiusKm: 24341, MassKg: 1.02413e26, Albedo: 0.442, Colour: "#4B70DD",
		Pole: Pole{RA0: 299.36, Dec0: 43.46, W0: 253.18, WDot: 536.3128492,
			Lib: &Libration{Arg0: 357.85, ArgDot: 52.316, RAAmp: 0.70, DecAmp: -0.51, WAmp: -0.48}},
		Orbit: specFromStandish(astro.BodyNeptune),
		Ring: &Ring{InnerKm: 41900, OuterKm: 62933, Faint: true,
			Source: "Adams ring and inner arcs, Voyager 2"},
		Texture: "neptune", Tier: TierFitted, Source: srcPhysical + "; " + srcPole,
	}}
}
