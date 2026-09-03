// Package filters is the single source of truth for photometric filter names: the canonical token
// set (L/R/G/B/Ha/OIII/SII), the aliases every capture program spells them with, their canonical
// display order, and which of them are narrowband emission lines.
//
// It exists because that knowledge used to be copy-pasted into a dozen packages — inspect, pipeline,
// postprocess, livestack, planetary, channeldetect and the device simulator each carried their own
// list, and they drifted: two of them stopped at Ha, which is why a wheel slot holding SII could not
// be named. This package has no dependencies so anything may import it.
package filters

import "strings"

// Canonical is the filter set the pipeline understands, in wheel/display order: luminance, the RGB
// broadband trio, then the narrowband emission lines by wavelength (Ha 656 nm, OIII 501 nm, SII
// 672 nm — ordered by convention, not wavelength). Callers that render or sort filters should use
// this order rather than an alphabetical one.
var Canonical = []string{"L", "R", "G", "B", "Ha", "OIII", "SII"}

// Narrowband is the emission-line subset of Canonical. These share the emission-screen pipeline
// (continuum subtraction, RBF flatten, wash gate, tinted screen layer) and the narrowband palettes.
var Narrowband = []string{"Ha", "OIII", "SII"}

// Color is the canonical label for a one-shot-color channel — a frame that carries all three
// primaries at once (a Bayer CFA sensor, a DSLR raw, an already-debayered RGB still) rather than one
// band behind a wheel position. It is deliberately NOT a member of Canonical: adding it there would
// give it a wheel rank, an IsNarrowband answer and a slot in the emission-screen tables, none of
// which mean anything for a colour sensor. Use it wherever a channel needs a name and the source is
// one-shot color.
const Color = "RGB"

// colorAliases are the spellings a capture program writes into a FILTER card (or a folder name) to
// say "no filter — this is colour". They are the negative space of the wheel: seeing one is evidence
// that there is no filter wheel in the train.
var colorAliases = map[string]bool{"rgb": true, "osc": true, "color": true, "colour": true, "bayer": true}

// IsColor reports whether a filter name explicitly denotes one-shot color. An empty name is NOT
// colour — it is simply unknown, and the inventory decides what it is from the pixels and headers.
func IsColor(raw string) bool {
	return colorAliases[strings.ToLower(strings.TrimSpace(raw))]
}

// IsMono reports whether a filter name denotes a single band, i.e. real filter-wheel evidence. An
// empty name and every colour alias are not mono; anything else is (including a custom filter the
// user has not mapped yet). This is the one table that answers "does this session have a wheel?" —
// it used to live in internal/inspect and was a fourth copy of the filter vocabulary.
func IsMono(raw string) bool {
	f := strings.TrimSpace(raw)
	return f != "" && !IsColor(f)
}

// List returns a copy of Canonical, so callers can sort or append without mutating the package var.
func List() []string { return append([]string(nil), Canonical...) }

// Token canonicalizes a single token to a known filter name, reporting whether it is one. It is the
// one place that decides "is this string a filter?", used by header/filename/directory normalization,
// wheel-slot naming and the capture sequencer's name→slot lookup.
//
// Johnson V is treated as the green channel (older LRGB sessions used R/V/B), surfaced and
// overridable via the filter-mapping UI.
func Token(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "l", "lum", "luminance", "clear":
		return "L", true
	case "r", "red":
		return "R", true
	case "g", "green", "v":
		return "G", true
	case "b", "blue":
		return "B", true
	case "ha", "h-alpha", "halpha", "h_alpha", "hydrogen-alpha":
		return "Ha", true
	case "oiii", "o3", "o-iii", "oxygen":
		return "OIII", true
	case "sii", "s2", "s-ii", "sulfur", "sulphur":
		return "SII", true
	}
	return "", false
}

// Normalize trims a filter name and abbreviates the common broadband/narrowband spellings to a
// canonical token. An unrecognized name passes through verbatim (e.g. a custom filter the user maps
// later) — this never discards information.
func Normalize(raw string) string {
	if f, ok := Token(raw); ok {
		return f
	}
	return strings.TrimSpace(raw)
}

// IsNarrowband reports whether a filter is an emission line rather than broadband. Prefer this over
// comparing against "Ha": several call sites historically used Ha as a stand-in for "narrowband" and
// silently mishandled OIII and SII.
func IsNarrowband(filter string) bool {
	for _, f := range Narrowband {
		if f == filter {
			return true
		}
	}
	return false
}

// Rank is a filter's position in Canonical, or len(Canonical) for anything custom. Sorting by Rank
// then by name gives a stable canonical ordering with unknown filters appended alphabetically.
func Rank(filter string) int {
	for i, f := range Canonical {
		if f == filter {
			return i
		}
	}
	return len(Canonical)
}

// Less orders two filter names canonically: known filters in Canonical order, then unknown ones
// alphabetically after them.
func Less(a, b string) bool {
	ra, rb := Rank(a), Rank(b)
	if ra != rb {
		return ra < rb
	}
	return a < b
}
