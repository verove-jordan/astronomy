package skyplan

import "github.com/verove-jordan/astronomy/internal/skycat"

// Composition is the emission-line makeup of a target, for filter-wheel planning. Line strengths are
// relative 0..1 values (for bar display, not photometry). It is curated for popular targets and
// otherwise derived from the object type; Source records which.
type Composition struct {
	Ha        float64  `json:"ha"`
	OIII      float64  `json:"oiii"`
	SII       float64  `json:"sii"`
	Hb        float64  `json:"hb,omitempty"`
	NII       float64  `json:"nii,omitempty"`
	Broadband bool     `json:"broadband"`
	Palette   string   `json:"palette"` // suggested processing palette, e.g. SHO / HOO / LRGB
	Filters   []string `json:"filters"` // filters worth loading (tokens: L/R/G/B/Ha/OIII/SII)
	Source    string   `json:"source"`  // "curated" | "typical"
	Note      string   `json:"note,omitempty"`
}

var (
	shoFilters  = []string{"Ha", "OIII", "SII"}
	hooFilters  = []string{"OIII", "Ha"}
	lrgbFilters = []string{"L", "R", "G", "B"}
	lrgbHa      = []string{"L", "R", "G", "B", "Ha"}
)

// compositionFor returns the curated composition for a known target (matched by primary name or any
// alias), else the typical composition for its derived type.
func compositionFor(rec skycat.Record, objType string) Composition {
	for _, name := range append([]string{rec.Name}, rec.Aliases...) {
		if c, ok := curatedComposition[skycat.Normalize(name)]; ok {
			c.Source = "curated"
			return c
		}
	}
	c := typicalComposition(objType)
	c.Source = "typical"
	return c
}

// typicalComposition is the type-based fallback when a target is not in the curated set.
func typicalComposition(objType string) Composition {
	switch objType {
	case "emission_nebula":
		return Composition{Ha: 1.0, OIII: 0.4, SII: 0.5, Palette: "SHO", Filters: shoFilters, Note: "HII / emission region — Hα-dominant."}
	case "planetary_nebula":
		return Composition{Ha: 0.8, OIII: 1.0, SII: 0.25, Palette: "HOO", Filters: hooFilters, Note: "Planetary nebula — OIII-bright."}
	case "supernova_remnant":
		return Composition{Ha: 0.8, OIII: 0.9, SII: 0.6, Palette: "SHO", Filters: shoFilters, Note: "Supernova remnant — strong OIII & Hα."}
	case "nebula":
		return Composition{Ha: 0.8, OIII: 0.4, SII: 0.4, Palette: "SHO", Filters: shoFilters, Note: "Emission nebula — narrowband-friendly."}
	case "dark_nebula":
		return Composition{Ha: 0.15, OIII: 0.05, SII: 0.05, Broadband: true, Palette: "RGB", Filters: lrgbFilters, Note: "Dark nebula — broadband (silhouette)."}
	default: // galaxy, globular, cluster, other
		return Composition{Ha: 0.1, OIII: 0.05, SII: 0.05, Broadband: true, Palette: "LRGB", Filters: lrgbFilters, Note: "Broadband target — little narrowband emission."}
	}
}

func sho(ha, oiii, sii float64, note string) Composition {
	return Composition{Ha: ha, OIII: oiii, SII: sii, Palette: "SHO", Filters: shoFilters, Note: note}
}

func hoo(ha, oiii float64, note string) Composition {
	return Composition{Ha: ha, OIII: oiii, SII: 0.2, Palette: "HOO", Filters: hooFilters, Note: note}
}

var veil = Composition{Ha: 0.7, OIII: 0.95, SII: 0.3, Palette: "HOO", Filters: hooFilters, Note: "Veil — OIII-rich SNR; HOO excellent (SII weak)."}

// curatedComposition maps normalized designations (skycat.Normalize form) to known palettes for
// popular targets. Extend freely — anything not listed falls back to typicalComposition.
var curatedComposition = map[string]Composition{
	// Planetary nebulae (OIII-bright → HOO)
	"M27":     hoo(0.85, 1.0, "Dumbbell — OIII-bright; HOO shines."),
	"M57":     hoo(0.7, 1.0, "Ring — strong OIII."),
	"M76":     hoo(0.8, 0.95, "Little Dumbbell."),
	"M97":     hoo(0.7, 1.0, "Owl — OIII-bright."),
	"NGC6543": hoo(0.7, 1.0, "Cat's Eye."),
	"NGC7662": hoo(0.6, 1.0, "Blue Snowball."),
	"NGC7293": sho(1.0, 0.9, 0.4, "Helix — strong Hα & OIII."),
	// Supernova remnants
	"NGC6960": veil, "NGC6992": veil, "NGC6995": veil, "NGC6979": veil,
	"IC443":  sho(1.0, 0.5, 0.6, "Jellyfish SNR."),
	"SH2240": sho(0.8, 0.6, 0.4, "Spaghetti (Simeis 147)."),
	// HII / emission nebulae (Hα-dominant → SHO)
	"NGC7000": sho(1.0, 0.4, 0.5, "North America — classic SHO."),
	"IC5070":  sho(1.0, 0.4, 0.5, "Pelican."),
	"IC1396":  sho(1.0, 0.3, 0.6, "Elephant's Trunk."),
	"NGC2237": sho(1.0, 0.5, 0.6, "Rosette."),
	"NGC2244": sho(1.0, 0.5, 0.6, "Rosette."),
	"IC1805":  sho(1.0, 0.4, 0.6, "Heart."),
	"IC1848":  sho(1.0, 0.4, 0.6, "Soul."),
	"NGC7635": sho(1.0, 0.6, 0.6, "Bubble."),
	"NGC6888": sho(0.9, 0.8, 0.4, "Crescent — strong OIII too."),
	"NGC1499": sho(1.0, 0.3, 0.4, "California."),
	"IC405":   sho(1.0, 0.4, 0.4, "Flaming Star."),
	"IC410":   sho(1.0, 0.4, 0.6, "Tadpoles."),
	"IC2177":  sho(1.0, 0.4, 0.5, "Seagull."),
	"NGC2264": sho(1.0, 0.4, 0.5, "Cone / Christmas Tree."),
	"NGC2174": sho(1.0, 0.4, 0.5, "Monkey Head."),
	"SH2155":  sho(1.0, 0.4, 0.5, "Cave."),
	"NGC7380": sho(1.0, 0.5, 0.6, "Wizard."),
	"NGC281":  sho(1.0, 0.4, 0.6, "Pacman."),
	"IC434":   sho(1.0, 0.3, 0.4, "Horsehead region."),
	"NGC2024": sho(1.0, 0.3, 0.4, "Flame."),
	"IC5146":  sho(1.0, 0.3, 0.4, "Cocoon."),
	"NGC6334": sho(1.0, 0.4, 0.6, "Cat's Paw."),
	"NGC6357": sho(1.0, 0.4, 0.6, "Lobster."),
	"NGC3372": sho(1.0, 0.5, 0.6, "Carina."),
	"M42":     {Ha: 1.0, OIII: 0.7, SII: 0.4, Palette: "SHO", Filters: shoFilters, Note: "Orion — superb in SHO and RGB."},
	"M43":     sho(1.0, 0.5, 0.4, "De Mairan's (Orion)."),
	"M8":      sho(1.0, 0.5, 0.5, "Lagoon."),
	"M16":     sho(1.0, 0.5, 0.6, "Eagle / Pillars."),
	"M17":     sho(1.0, 0.5, 0.6, "Omega / Swan."),
	"M20":     {Ha: 1.0, OIII: 0.4, SII: 0.4, Palette: "SHO", Filters: shoFilters, Note: "Trifid — emission + reflection."},
	// Galaxies with bright HII regions — broadband, but Hα adds detail
	"M33": {Ha: 0.3, Broadband: true, Palette: "LRGB+Ha", Filters: lrgbHa, Note: "Triangulum — Hα reveals HII regions."},
	"M31": {Ha: 0.2, Broadband: true, Palette: "LRGB+Ha", Filters: lrgbHa, Note: "Andromeda — Hα boosts HII knots."},
}
