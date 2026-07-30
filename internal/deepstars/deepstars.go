// Package deepstars embeds a slimmed HYG-derived deep star catalogue (mag ≤ 9, ~110k stars) for
// the star-name annotation: positions, magnitudes, proper motions and the designation chain
// (proper name, Bayer, Flamsteed, HD). Positions are J2000/ICRS — the same frame Siril plate
// solutions use — so lookups apply proper motion only, never precession. Regenerate the data with
// `just gen-deepstars-data` (provenance + licence in catalogue/README.md).
package deepstars

import (
	"compress/gzip"
	"embed"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
)

//go:embed catalogue/hyg_mag9.csv.gz
var embedded embed.FS

// Star is one catalogue entry (J2000 position, mas/yr proper motion).
type Star struct {
	RADeg, DecDeg float64
	Mag           float64
	Proper        string  // "Vega" or ""
	Bayer         string  // HYG token, e.g. "Alp" or "The-2", or ""
	Flam          int     // Flamsteed number, 0 when absent
	Con           string  // 3-letter IAU constellation, e.g. "Lyr"
	HD            int     // Henry Draper number, 0 when absent
	PMRA, PMDec   float64 // proper motion, mas/yr (PMRA includes the cos δ factor, Hipparcos-style)
}

var (
	loadOnce sync.Once
	catalog  []Star
	loadErr  error
)

// j2000 is the catalogue epoch (JD 2451545.0).
var j2000 = time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)

func load() ([]Star, error) {
	loadOnce.Do(func() {
		f, err := embedded.Open("catalogue/hyg_mag9.csv.gz")
		if err != nil {
			loadErr = fmt.Errorf("deepstars: open embedded catalogue: %w", err)
			return
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			loadErr = fmt.Errorf("deepstars: gunzip catalogue: %w", err)
			return
		}
		defer gz.Close()
		catalog, loadErr = parseCatalog(gz)
		if loadErr == nil {
			sort.Slice(catalog, func(i, j int) bool { return catalog[i].Mag < catalog[j].Mag })
		}
	})
	return catalog, loadErr
}

// parseCatalog reads the slimmed CSV (header-driven; columns as written by the generator).
func parseCatalog(r io.Reader) ([]Star, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return rec[i]
		}
		return ""
	}
	var stars []Star
	for {
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("read row: %w", e)
		}
		ra, e1 := strconv.ParseFloat(get(rec, "ra_deg"), 64)
		dec, e2 := strconv.ParseFloat(get(rec, "dec_deg"), 64)
		mag, e3 := strconv.ParseFloat(get(rec, "mag"), 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, fmt.Errorf("malformed row %q", rec)
		}
		s := Star{
			RADeg: ra, DecDeg: dec, Mag: mag,
			Proper: get(rec, "proper"),
			Bayer:  get(rec, "bayer"),
			Con:    get(rec, "con"),
		}
		s.Flam, _ = strconv.Atoi(get(rec, "flam"))
		s.HD, _ = strconv.Atoi(get(rec, "hd"))
		s.PMRA, _ = strconv.ParseFloat(get(rec, "pmra"), 64)
		s.PMDec, _ = strconv.ParseFloat(get(rec, "pmdec"), 64)
		stars = append(stars, s)
	}
	return stars, nil
}

// Count returns the catalogue size (0 when the embedded data fails to load).
func Count() int {
	stars, err := load()
	if err != nil {
		return 0
	}
	return len(stars)
}

// pmMarginDeg bounds how far proper motion can carry any star from its J2000 position over the
// epochs this app sees (~10″/yr for the fastest mover × decades ≈ arcminutes).
const pmMarginDeg = 0.1

// InField returns the catalogue stars within radiusDeg of the field center at the given epoch,
// magnitude-ascending, capped at maxN (maxN ≤ 0 → uncapped). Positions are advanced by proper
// motion from J2000 to epoch; RA wrap and pole fields are handled by unit-vector math.
func InField(centerRADeg, centerDecDeg, radiusDeg float64, maxN int, epoch time.Time) []Star {
	stars, err := load()
	if err != nil {
		return nil
	}
	const degRad = math.Pi / 180
	years := epoch.Sub(j2000).Hours() / 24 / 365.25
	sinD0, cosD0 := math.Sincos(centerDecDeg * degRad)
	cosR := math.Cos(radiusDeg * degRad)

	var out []Star
	for _, s := range stars {
		if math.Abs(s.DecDeg-centerDecDeg) > radiusDeg+pmMarginDeg {
			continue
		}
		s.DecDeg += s.PMDec / 3.6e6 * years
		if c := math.Cos(s.DecDeg * degRad); c > 1e-6 {
			s.RADeg += s.PMRA / 3.6e6 * years / c
		}
		sinD, cosD := math.Sincos(s.DecDeg * degRad)
		dot := sinD*sinD0 + cosD*cosD0*math.Cos((s.RADeg-centerRADeg)*degRad)
		if dot < cosR {
			continue
		}
		out = append(out, s)
		if maxN > 0 && len(out) == maxN {
			break
		}
	}
	return out
}
