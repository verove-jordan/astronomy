package skyplan

import (
	"strings"

	"github.com/verove-jordan/astronomy/internal/skycat"
)

// DeriveType resolves the display object type. It prefers the authoritative OpenNGC type code
// (overlaid onto NGC/IC/M records by internal/skycat) and falls back to the source catalog +
// name/alias keywords for rows OpenNGC does not cover (Sh2/LDN and un-typed entries). Used only for
// display, filtering and the Moon-sensitivity heuristic — never directly in the score. Values match
// the frontend's SkyObjectType vocabulary.
func DeriveType(rec skycat.Record) string {
	if t := mapOpenNGCType(rec.Type); t != "" {
		return t
	}
	switch rec.Source {
	case "sh2":
		return "emission_nebula"
	case "ldn":
		return "dark_nebula"
	}
	hay := strings.ToLower(rec.Name + " " + strings.Join(rec.Aliases, " "))
	switch {
	case strings.Contains(hay, "globular"):
		return "globular"
	case strings.Contains(hay, "planetary"):
		return "planetary_nebula"
	case strings.Contains(hay, "supernova"), strings.Contains(hay, "remnant"):
		return "supernova_remnant"
	case strings.Contains(hay, "galaxy"):
		return "galaxy"
	case strings.Contains(hay, "cluster"):
		return "cluster"
	case strings.Contains(hay, "nebula"):
		return "nebula"
	}
	return "other"
}

// mapOpenNGCType maps an OpenNGC type code to the display vocabulary, or "" when the code is empty or
// not a deep-sky imaging type (stars/doubles/novae are left to the keyword fallback → "other").
func mapOpenNGCType(code string) string {
	switch code {
	case "G", "GPair", "GTrpl", "GGroup":
		return "galaxy"
	case "OCl":
		return "open_cluster"
	case "GCl":
		return "globular"
	case "PN":
		return "planetary_nebula"
	case "SNR":
		return "supernova_remnant"
	case "HII", "EmN", "Cl+N":
		return "emission_nebula"
	case "RfN":
		return "reflection_nebula"
	case "Neb":
		return "nebula"
	case "DrkN":
		return "dark_nebula"
	case "*Ass":
		return "cluster"
	}
	return ""
}
