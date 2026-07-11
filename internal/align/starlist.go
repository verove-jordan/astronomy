package align

import (
	"embed"
	"encoding/csv"
	"io"
	"strings"
)

// Hand-controller star lists: which catalog stars a mount's GoTo hand controller can actually
// offer for alignment, and the exact label it displays for each (see starlists/README.md for
// provenance). A profile opts in via Profile.StarList; an empty key means no restriction.

//go:embed starlists/*.csv
var starListsFS embed.FS

// starLists maps a Profile.StarList key to lower(catalog name) → exact hand-controller label,
// parsed once at package load. Rows whose catalog name doesn't resolve in the bright-star
// catalog are skipped (starlist_test.go asserts none are).
var starLists = map[string]map[string]string{
	"celestron": loadStarList("starlists/celestron.csv"),
	"synscan":   loadStarList("starlists/synscan.csv"),
}

// loadStarList parses one embedded CSV (header: hc_name,catalog_name; empty catalog_name means
// identical to hc_name). Malformed or unresolvable rows are skipped, mirroring loadCatalog.
func loadStarList(path string) map[string]string {
	data, err := starListsFS.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	inCatalog := make(map[string]bool, len(catalog))
	for _, s := range catalog {
		inCatalog[strings.ToLower(s.Name)] = true
	}

	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = 2
	r.TrimLeadingSpace = true

	out := make(map[string]string, 128)
	header := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue // skip a malformed row, keep reading the rest
		}
		if header {
			header = false
			continue
		}
		hcName := strings.TrimSpace(rec[0])
		catalogName := strings.TrimSpace(rec[1])
		if catalogName == "" {
			catalogName = hcName
		}
		key := strings.ToLower(catalogName)
		if hcName == "" || !inCatalog[key] {
			continue
		}
		out[key] = hcName
	}
	return out
}

// inStarList reports whether a catalog star is selectable on the profile's hand controller.
// An empty listKey means no restriction (every star qualifies).
func inStarList(listKey, catalogName string) bool {
	if listKey == "" {
		return true
	}
	_, ok := starLists[listKey][strings.ToLower(catalogName)]
	return ok
}

// hcLabel returns the exact hand-controller label for a catalog star, or "" when the profile
// has no list or the star is not on it.
func hcLabel(listKey, catalogName string) string {
	if listKey == "" {
		return ""
	}
	return starLists[listKey][strings.ToLower(catalogName)]
}
