// Package skycat resolves a deep-sky object name (e.g. "M101", "NGC5457") to J2000 coordinates
// using Siril's bundled object catalogues, so plate-solving + SPCC can run on captures whose FITS
// headers carry no RA/Dec (only a folder/object name).
package skycat

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
)

// catalogFiles are searched in order; Messier first (most common targets), then the larger lists.
var catalogFiles = []string{"messier.csv", "ngc.csv", "ic.csv", "sh2.csv"}

// Resolve returns "ra,dec" (decimal degrees) for the named object, matched by primary name or alias
// against the catalogues under dir. ok is false when dir/name is empty or the object is not found.
func Resolve(name, dir string) (coords string, ok bool) {
	key := normalize(name)
	if key == "" || dir == "" {
		return "", false
	}
	for _, f := range catalogFiles {
		if c, found := lookup(filepath.Join(dir, f), key); found {
			return c, true
		}
	}
	return "", false
}

func lookup(path, key string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	r := csv.NewReader(file)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return "", false
	}
	for i, row := range rows {
		if i == 0 || len(row) < 3 { // skip the header and malformed rows
			continue
		}
		coords := row[1] + "," + row[2]
		if normalize(row[0]) == key {
			return coords, true
		}
		if len(row) >= 6 {
			for _, alias := range strings.Split(row[5], "/") {
				if normalize(alias) == key {
					return coords, true
				}
			}
		}
	}
	return "", false
}

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
