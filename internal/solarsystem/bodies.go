package solarsystem

import "sync"

// Pole is a body's rotational state in the IAU/IAG WGCCRE form: where its north pole points at an
// instant, and how far its prime meridian has turned. This is the whole of "rotation and axis" —
// it is what tilts Uranus on its side, puts Earth's terminator where the real one is, and turns
// Jupiter's Great Red Spot round every ten hours.
type Pole struct {
	RA0    float64 `json:"ra0_deg"`           // right ascension of the north pole, J2000
	RADot  float64 `json:"ra_dot,omitempty"`  // degrees per Julian century
	Dec0   float64 `json:"dec0_deg"`          // declination of the north pole, J2000
	DecDot float64 `json:"dec_dot,omitempty"` // degrees per Julian century
	W0     float64 `json:"w0_deg"`            // prime meridian at J2000
	WDot   float64 `json:"w_dot"`             // degrees per day; negative for a retrograde rotator

	// Lib is the one periodic term some bodies' rotational elements carry. Most are far below
	// anything a map of the system can show, but Neptune's swings its pole by half a degree — more
	// than the linear terms move it in a century — so it is not optional there.
	Lib *Libration `json:"libration,omitempty"`
}

// Libration is a single periodic term of the IAU rotational elements: one argument, applied to the
// pole's right ascension as a sine, to its declination as a cosine, and to the prime meridian as a
// sine — the form the IAU report tabulates.
type Libration struct {
	Arg0   float64 `json:"arg0_deg"`    // argument at J2000
	ArgDot float64 `json:"arg_dot"`     // degrees per Julian century
	RAAmp  float64 `json:"ra_amp_deg"`  //
	DecAmp float64 `json:"dec_amp_deg"` //
	WAmp   float64 `json:"w_amp_deg"`   //
}

// RotationHours is the body's sidereal rotation period in hours, negative when retrograde.
func (p Pole) RotationHours() float64 {
	if p.WDot == 0 {
		return 0
	}
	return 360 / p.WDot * 24
}

// Ring describes a ring system as the annulus it is drawn as. Radii are from the planet's centre.
type Ring struct {
	InnerKm float64 `json:"inner_km"`
	OuterKm float64 `json:"outer_km"`
	Texture string  `json:"texture,omitempty"` // alpha/colour map key, when one exists
	Faint   bool    `json:"faint"`             // the dark, narrow systems: Jupiter, Uranus, Neptune
	Source  string  `json:"source"`
}

// Body is one world: what it physically is, which way it spins, and how it moves.
type Body struct {
	Key    string `json:"key"`              // canonical lowercase name; the i18n key too
	Kind   Kind   `json:"kind"`             //
	Parent string `json:"parent,omitempty"` // the body it orbits, for moons

	RadiusKm      float64 `json:"radius_km"`                 // equatorial
	PolarRadiusKm float64 `json:"polar_radius_km,omitempty"` // when measurably flattened
	MassKg        float64 `json:"mass_kg,omitempty"`         //
	Albedo        float64 `json:"albedo,omitempty"`          // geometric
	Colour        string  `json:"colour"`                    // hex: label/orbit tint, and the procedural fallback

	Pole  Pole  `json:"pole"`
	Ring  *Ring `json:"ring,omitempty"`
	Orbit *Spec `json:"orbit,omitempty"` // nil for the Sun, and for anything driven by a Series

	// Series names a built-in analytic model for bodies whose motion no fixed element set describes
	// well. The browser mirrors each one by name; an unknown name means the body is simply not drawn,
	// which is why this is a string and not a bare boolean.
	Series  string `json:"series,omitempty"`
	Texture string `json:"texture,omitempty"`

	Tier   Tier   `json:"tier"`
	Source string `json:"source"`
}

// Flattening is the body's oblateness (0 for a sphere), used to draw the gas giants as the visibly
// squashed ellipsoids they are rather than as balls.
func (b Body) Flattening() float64 {
	if b.PolarRadiusKm <= 0 || b.RadiusKm <= 0 {
		return 0
	}
	return 1 - b.PolarRadiusKm/b.RadiusKm
}

var (
	registryOnce sync.Once
	registry     []Body
	byKey        map[string]Body
)

// All returns every body in the scene, in a stable order: the Sun, then each planet immediately
// followed by its own moons, then the dwarf planets. Comets are not here — they come from a live
// feed and change between runs.
func All() []Body {
	registryOnce.Do(buildRegistry)
	out := make([]Body, len(registry))
	copy(out, registry)
	return out
}

// Find returns one body by its canonical key.
func Find(key string) (Body, bool) {
	registryOnce.Do(buildRegistry)
	b, ok := byKey[key]
	return b, ok
}

// Moons returns the satellites of a planet, in increasing orbital distance.
func Moons(parent string) []Body {
	var out []Body
	for _, b := range All() {
		if b.Kind == KindMoon && b.Parent == parent {
			out = append(out, b)
		}
	}
	return out
}

func buildRegistry() {
	registry = append(registry, sunAndPlanets()...)
	registry = append(registry, moonTable()...)
	registry = append(registry, dwarfTable()...)

	// Interleave each planet's moons directly after it, so the UI's body list reads as the systems it
	// actually is rather than as two disconnected lists.
	ordered := make([]Body, 0, len(registry))
	for _, b := range registry {
		if b.Kind == KindMoon {
			continue
		}
		ordered = append(ordered, b)
		for _, m := range registry {
			if m.Kind == KindMoon && m.Parent == b.Key {
				ordered = append(ordered, m)
			}
		}
	}
	registry = ordered

	byKey = make(map[string]Body, len(registry))
	for _, b := range registry {
		byKey[b.Key] = b
	}
}
