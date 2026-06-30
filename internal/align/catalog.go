// Package align selects bright alignment stars for a telescope GoTo calibration: given an observer's
// site and time, a mount profile and a requested star count, it returns an ordered, geometrically
// well-spread set of named stars (with skip/accept support) to center while building the mount's
// pointing model. The math is pure (no I/O), mirroring internal/astro and internal/polaralign.
package align

import (
	_ "embed"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
)

// Star is one catalog entry: a named naked-eye star with its J2000 equatorial position and visual
// magnitude. Positions are precessed to the epoch of the request at selection time.
type Star struct {
	Name          string
	Constellation string
	RADeg         float64 // J2000 right ascension, degrees [0,360)
	DecDeg        float64 // J2000 declination, degrees [-90,90]
	Mag           float64 // apparent visual magnitude
}

//go:embed brightstars.csv
var catalogCSV string

// catalog is the embedded bright-star list, parsed once at package load. Rows that fail to parse are
// skipped rather than crashing the binary; the catalog test asserts the expected count and ranges.
var catalog = loadCatalog(catalogCSV)

// loadCatalog parses the embedded CSV (header: name,constellation,ra_j2000_deg,dec_j2000_deg,mag).
// Malformed rows are skipped so a single bad line never takes down the whole catalog.
func loadCatalog(data string) []Star {
	r := csv.NewReader(strings.NewReader(data))
	r.FieldsPerRecord = 5
	r.TrimLeadingSpace = true

	out := make([]Star, 0, 256)
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
		name := strings.TrimSpace(rec[0])
		ra, err1 := strconv.ParseFloat(strings.TrimSpace(rec[2]), 64)
		dec, err2 := strconv.ParseFloat(strings.TrimSpace(rec[3]), 64)
		mag, err3 := strconv.ParseFloat(strings.TrimSpace(rec[4]), 64)
		if name == "" || err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if ra < 0 || ra >= 360 || dec < -90 || dec > 90 {
			continue
		}
		out = append(out, Star{
			Name:          name,
			Constellation: strings.TrimSpace(rec[1]),
			RADeg:         ra,
			DecDeg:        dec,
			Mag:           mag,
		})
	}
	return out
}

// Catalog returns a copy of the loaded bright-star catalog (used by tests and any catalog listing).
func Catalog() []Star {
	return append([]Star(nil), catalog...)
}
