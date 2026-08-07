package scene3d

import (
	_ "embed"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/skycat"
)

// dsodist.csv is a curated distance table for the deep-sky objects a telescope actually gets
// pointed at. It exists because the object catalogues the app already loads have no distance
// column at all: OpenNGC gives type, size, position angle and surface brightness, but nothing
// about how far away anything is — and a billboard without a distance cannot be placed.
//
// Values are the commonly cited ones, converted from light-years and rounded to the precision the
// underlying measurement actually carries (a galaxy quoted as "23 million light-years" does not
// know its distance to the parsec). Small enough to embed; there is no download and no network.
//
//go:embed dsodist.csv
var dsoDistCSV string

// dsoDistances is the parsed table, keyed by every normalised designation an object answers to —
// primary name and aliases both, because a label may arrive as "M1" or as "NGC1952" depending on
// which catalogue won the name.
var dsoDistances = sync.OnceValue(loadDSODistances)

func loadDSODistances() map[string]float64 {
	out := map[string]float64{}
	r := csv.NewReader(strings.NewReader(dsoDistCSV))
	r.FieldsPerRecord = 3
	if _, err := r.Read(); err != nil { // header
		return out
	}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A malformed row must not take the whole table down: the rest is still useful, and a
			// missing billboard is a far smaller failure than no 3D scene at all.
			continue
		}
		pc, err := strconv.ParseFloat(rec[1], 64)
		if err != nil || pc <= 0 {
			continue
		}
		for _, name := range append([]string{rec[0]}, strings.Split(rec[2], "/")...) {
			if k := skycat.Normalize(name); k != "" {
				// First writer wins, so a primary name is never shadowed by another object's alias.
				if _, seen := out[k]; !seen {
					out[k] = pc
				}
			}
		}
	}
	return out
}

// tableDistance looks up a catalogued distance for an object by any of the names it is labelled
// with. 0 means the table does not know it — which is common and not an error; that object simply
// gets no billboard.
func tableDistance(names ...string) float64 {
	table := dsoDistances()
	for _, n := range names {
		if d, ok := table[skycat.Normalize(n)]; ok {
			return d
		}
	}
	return 0
}
