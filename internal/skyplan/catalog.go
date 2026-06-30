package skyplan

import (
	"strings"

	"github.com/verove-jordan/astronomy/internal/skycat"
)

// deriveType infers a coarse object type from the source catalog and name/alias keywords. Siril's
// CSVs carry no type column, so this is best-effort: used only for display, filtering and the Moon
// sensitivity heuristic — never directly in the score. Values match the frontend's type vocabulary.
func deriveType(rec skycat.Record) string {
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
