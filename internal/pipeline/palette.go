// Colour-palette engine: maps the captured filters (L/R/G/B/Ha/OIII/SII) onto the RGB base of the
// deep-sky finish. "natural" (the empty default) is broadband LRGB with an Hα screen + SPCC; hargb is
// the same with Hα mandatory; the narrowband palettes (HOO / SHO-Hubble / HOS-CFHT / Foraxx) assign
// emission-line channels straight to R/G/B and skip SPCC. A palette whose required filters are absent
// walks a fallback chain to one that resolves (ultimately mono) and records a run.json note — so a user
// can pick SHO today and it simply renders natural until OIII/SII data exists. Consumed by prepGimpInputs.
package pipeline

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// paletteSpec declares one named palette: the source FILTER for each RGB slot (G left "" when GExpr
// builds a synthetic green), the filters it needs, the palette to fall back to when one is missing, and
// whether it is a mapped-narrowband palette (channels→RGB, no SPCC / L luminance / Hα screen) or mono.
type paletteSpec struct {
	R, G, B    string
	GExpr      string // synthetic-green pixel math with $Filter$ tokens (dynamic palettes); "" → direct G
	Requires   []string
	Fallback   string // "" → mono
	Narrowband bool
	Mono       bool
}

// paletteSpecs is the palette catalogue. Foraxx's green is the per-pixel geometric mean of Hα and OIII
// — the Webb-style dynamic blend approximated with Siril pixel math.
var paletteSpecs = map[string]paletteSpec{
	"natural": {R: "R", G: "G", B: "B", Requires: []string{"R", "G", "B"}, Fallback: "mono"},
	"hargb":   {R: "R", G: "G", B: "B", Requires: []string{"R", "G", "B", "Ha"}, Fallback: "natural"},
	"hoo":     {R: "Ha", G: "OIII", B: "OIII", Requires: []string{"Ha", "OIII"}, Fallback: "natural", Narrowband: true},
	"sho":     {R: "SII", G: "Ha", B: "OIII", Requires: []string{"SII", "Ha", "OIII"}, Fallback: "hoo", Narrowband: true},
	"hos":     {R: "Ha", G: "OIII", B: "SII", Requires: []string{"Ha", "OIII", "SII"}, Fallback: "hoo", Narrowband: true},
	"foraxx":  {R: "Ha", GExpr: "($Ha$ * $OIII$)^0.5", B: "OIII", Requires: []string{"Ha", "OIII"}, Fallback: "hoo", Narrowband: true},
	"mono":    {Mono: true},
}

// PaletteNames returns the palette names in display order (the UI selector + knob menu).
func PaletteNames() []string {
	return []string{"natural", "hargb", "hoo", "sho", "hos", "foraxx", "mono"}
}

// isPaletteName reports whether s (lower-cased, trimmed) is a known palette; empty is valid (natural).
func isPaletteName(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return true
	}
	_, ok := paletteSpecs[s]
	return ok
}

// paletteResolved is a palette resolved against the channels actually present in a run.
type paletteResolved struct {
	Name       string // the palette actually applied (after any fallback)
	R, G, B    string // FILTER names feeding the rgbcomp slots; when Mono, R is the single source and G/B ""
	GExpr      string // synthetic-green pixel math (aligned basenames substituted); "" → G is a direct channel
	Color      bool   // false → the mono base branch
	UseLum     bool   // blend the L luminance layer
	HaScreen   bool   // screen Hα as a red layer (broadband only)
	OIIIScreen bool   // screen [OIII] as a teal layer (broadband only, and only when OIII survived as its own channel — no-B runs fold it into blue instead)
	SIIScreen  bool   // screen [SII] as a deep-red/gold layer (broadband only)
	Narrowband bool   // mapped narrowband: skip SPCC, unlinked stretch, no SCNR, treat as pre-calibrated
	Mono       bool
}

// screenOnly reports whether a filter contributes ONLY as an additive emission screen in this
// palette, never as a base R/G/B/L channel. Such a layer fades where its nights did not reach
// instead of leaving a hole, so it must not constrain anything that reasons about coverage.
//
// It compares against the resolved slots rather than testing "is it narrowband": under a narrowband
// palette Ha/OIII/SII ARE the base channels, and a no-B run folds OIII into the blue slot.
func (p paletteResolved) screenOnly(filter string) bool {
	switch filter {
	case "Ha":
		if !p.HaScreen {
			return false
		}
	case "OIII":
		if !p.OIIIScreen {
			return false
		}
	case "SII":
		if !p.SIIScreen {
			return false
		}
	default:
		return false
	}
	return filter != p.R && filter != p.G && filter != p.B
}

// resolvePalette maps a preset's chosen palette onto the channels present, walking the fallback chain
// when a required filter is missing, and returns the resolved assignment plus a run.json note describing
// any fallback. A nil/empty/unknown palette is "natural".
func resolvePalette(p *mode.Preset, channels map[string]string) (paletteResolved, string) {
	// One-shot color has no filters to map. Every entry in paletteSpecs is written in terms of filter
	// names (Ha→R, OIII→G …), so the fallback chain below would find none of them present, walk all the
	// way to "mono", and throw the colour away. A colour run passes its single RGB channel through.
	if isColorRun(p) {
		return colorPalette(), ""
	}
	has := func(f string) bool { _, ok := channels[f]; return ok }
	want := "natural"
	if p != nil {
		if s := strings.ToLower(strings.TrimSpace(p.Palette)); s != "" {
			if _, ok := paletteSpecs[s]; ok {
				want = s
			}
		}
	}
	name := want
	for !paletteSpecs[name].Mono && !haveAll(has, paletteSpecs[name].Requires) {
		if fb := paletteSpecs[name].Fallback; fb != "" {
			name = fb
		} else {
			name = "mono"
		}
	}
	var note string
	if name != want {
		note = fmt.Sprintf("palette %q unavailable (missing %s) — using %q",
			want, strings.Join(missingFilters(has, paletteSpecs[want].Requires), "+"), name)
	}
	spec := paletteSpecs[name]
	if spec.Mono {
		return resolveMono(channels, name), note
	}
	res := paletteResolved{Name: name, R: spec.R, G: spec.G, B: spec.B, Color: true, Narrowband: spec.Narrowband}
	if spec.GExpr != "" {
		res.GExpr = substituteFilters(spec.GExpr, channels)
		res.G = "" // built by pixel math, not a direct channel
	}
	if !spec.Narrowband { // broadband LRGB: L luminance + optional Hα/[OIII]/[SII] emission screens
		res.UseLum = has("L")
		res.HaScreen = has("Ha")
		// OIII reaches here as its own channel only when a real B exists (otherwise channelMastersMap
		// already folded it into the blue base — screening it too would double-count the line).
		res.OIIIScreen = has("OIII")
		// SII is never folded into a base slot, so it is available whenever it was captured.
		res.SIIScreen = has("SII")
	}
	return res, note
}

// paletteWantsOIIIAsBlue reports whether channelMastersMap should fold OIII into the blue slot when no B
// filter is present (the natural-family convenience). A mapped-narrowband palette consumes OIII as its
// own base channel, so it must keep OIII distinct for resolvePalette to find it.
func paletteWantsOIIIAsBlue(p *mode.Preset) bool {
	if p == nil || p.Palette == "" {
		return true
	}
	spec, ok := paletteSpecs[strings.ToLower(strings.TrimSpace(p.Palette))]
	if !ok {
		return true
	}
	return !spec.Narrowband
}

// resolveMono picks the single mono source: prefer L, then Hα, then the canonical filter order.
func resolveMono(channels map[string]string, name string) paletteResolved {
	for _, f := range append([]string{"L", "Ha"}, filterOrder...) {
		if _, ok := channels[f]; ok {
			return paletteResolved{Name: name, R: f, Mono: true}
		}
	}
	return paletteResolved{Name: name, Mono: true}
}

func haveAll(has func(string) bool, req []string) bool {
	for _, f := range req {
		if !has(f) {
			return false
		}
	}
	return true
}

func missingFilters(has func(string) bool, req []string) []string {
	var m []string
	for _, f := range req {
		if !has(f) {
			m = append(m, f)
		}
	}
	return m
}

// substituteFilters replaces each $Filter$ token in a pixel-math expression with the channel's aligned
// basename ($aligned_Ha$ etc.), so a palette's dynamic-green formula references the real loaded images.
func substituteFilters(expr string, channels map[string]string) string {
	for _, f := range filterOrder {
		if bn, ok := channels[f]; ok {
			expr = strings.ReplaceAll(expr, "$"+f+"$", "$"+bn+"$")
		}
	}
	return expr
}
