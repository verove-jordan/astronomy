// Package skycat resolves a deep-sky object name (e.g. "M101", "NGC5457") to J2000 coordinates
// using Siril's bundled object catalogues, so plate-solving + SPCC can run on captures whose FITS
// headers carry no RA/Dec (only a folder/object name). When no readable on-disk catalogue is present
// (e.g. the Docker engine image, or a host without Siril) it falls back to an embedded snapshot — see
// Load and embed.go.
package skycat

import (
	"strconv"
	"strings"
)

// Resolve returns "ra,dec" (decimal degrees) for the named object, matched by primary name or alias
// against the catalogue under dir — falling back to the embedded snapshot when dir has no readable
// catalogue (see Load). ok is false when name is empty or the object is not found.
func Resolve(name, dir string) (coords string, ok bool) {
	if normalize(name) == "" {
		return "", false
	}
	rec, found := Load(dir).Lookup(name)
	if !found {
		return "", false
	}
	return formatCoord(rec.RADeg) + "," + formatCoord(rec.DecDeg), true
}

// formatCoord renders a coordinate in plain decimal degrees (shortest round-trip, no exponent).
func formatCoord(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// normalize upper-cases and strips non-alphanumerics so "M 101", "m-101" and "M101" all match.
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
