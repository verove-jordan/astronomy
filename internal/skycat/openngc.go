package skycat

import (
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// openNGCEntry is one slimmed OpenNGC row: the authoritative type/geometry/common-name overlay applied
// onto a Siril record of the same NGC/IC name. It carries no coordinates — the Siril record owns those.
type openNGCEntry struct {
	Type           string
	MajAx, MinAx   float64
	hasMaj, hasMin bool
	PosAng         float64
	hasPos         bool
	SurfBr         float64
	hasSurf        bool
	Hubble         string
	CommonNames    []string
}

// openNGCFile is the slim overlay CSV (see catalogue/README.md). It sits alongside the Siril CSVs and is
// loaded from the same directory / embedded FS.
const openNGCFile = "openngc.csv"

// parseOpenNGC reads the overlay CSV into a map keyed by normalized NGC/IC name. Malformed rows are
// skipped, never fatal — the overlay only enriches, so a bad row must not break the base catalogue.
func parseOpenNGC(r io.Reader) (map[string]openNGCEntry, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, nil
	}
	cols := headerIndex(rows[0])
	out := make(map[string]openNGCEntry, len(rows)-1)
	for _, row := range rows[1:] {
		name := field(row, cols, "name")
		typ := field(row, cols, "type")
		if name == "" || typ == "" {
			continue
		}
		out[normalize(name)] = openNGCEntryFromRow(row, cols, typ)
	}
	return out, nil
}

// openNGCEntryFromRow builds one entry from a parsed row (its numeric cells are optional).
func openNGCEntryFromRow(row []string, cols map[string]int, typ string) openNGCEntry {
	e := openNGCEntry{Type: typ, Hubble: field(row, cols, "hubble")}
	e.MajAx, e.hasMaj = parseFloatField(row, cols, "majax")
	e.MinAx, e.hasMin = parseFloatField(row, cols, "minax")
	e.PosAng, e.hasPos = parseFloatField(row, cols, "posang")
	e.SurfBr, e.hasSurf = parseFloatField(row, cols, "surfbr")
	if c := field(row, cols, "common"); c != "" {
		for _, n := range strings.Split(c, "/") {
			if n = strings.TrimSpace(n); n != "" {
				e.CommonNames = append(e.CommonNames, n)
			}
		}
	}
	return e
}

// applyOpenNGC enriches every catalog record whose NGC/IC name (or alias) matches an overlay entry,
// filling type, ellipse geometry, surface brightness, morphology and common names in place.
func (c *Catalog) applyOpenNGC(overlay map[string]openNGCEntry) {
	if len(overlay) == 0 {
		return
	}
	for _, rec := range c.records {
		for _, k := range normalizedKeys(rec) {
			e, ok := overlay[k]
			if !ok {
				continue
			}
			enrich(rec, e)
			break // first matching designation wins (the record's primary NGC/IC name)
		}
	}
}

// enrich copies an overlay entry onto a record without clobbering data the Siril row already has for
// size (OpenNGC's MajAx is preferred for the ellipse, but a Siril diameter is kept if OpenNGC lacks one).
func enrich(rec *Record, e openNGCEntry) {
	rec.Type = e.Type
	rec.Morphology = e.Hubble
	rec.CommonNames = e.CommonNames
	if e.hasMaj {
		rec.DiameterArcmin, rec.HasDiameter = e.MajAx, true
	}
	if e.hasMin {
		rec.MinorAxisArcmin, rec.HasMinorAxis = e.MinAx, true
	}
	if e.hasPos {
		rec.PositionAngleDeg, rec.HasPositionAngle = e.PosAng, true
	}
	if e.hasSurf {
		rec.SurfBr, rec.HasSurfBr = e.SurfBr, true
	}
}

// field returns the trimmed value of a named column, or "" when absent.
func field(row []string, cols map[string]int, name string) string {
	if i, ok := cols[name]; ok && i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

// parseFloatField returns the parsed value of a named numeric column and whether it was present+valid.
func parseFloatField(row []string, cols map[string]int, name string) (float64, bool) {
	s := field(row, cols, name)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// loadOpenNGCFromDir parses the overlay CSV from an on-disk catalogue directory (best-effort).
func loadOpenNGCFromDir(dir string) map[string]openNGCEntry {
	f, err := os.Open(filepath.Join(dir, openNGCFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	m, err := parseOpenNGC(f)
	if err != nil {
		return nil
	}
	return m
}
