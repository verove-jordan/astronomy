package skycat

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Record is a full deep-sky catalog entry. Magnitude and diameter are optional because Siril's CSVs
// omit them for some objects (all of sh2/ldn, and scattered ngc/ic rows have empty fields), so each
// has a Has* flag rather than a sentinel value.
type Record struct {
	Name           string
	RADeg, DecDeg  float64
	DiameterArcmin float64
	HasDiameter    bool
	MagV           float64
	HasMag         bool
	Aliases        []string
	Source         string // catalog of origin: messier|ngc|ic|sh2|ldn
}

// Catalog is the merged, de-duplicated set of catalog records, with a normalized name/alias index.
type Catalog struct {
	records []*Record
	index   map[string]*Record
}

// catalogSources lists the Siril catalog files in load order. Messier first so its friendly names
// win as the primary designation when an object appears in several catalogs (e.g. M1/NGC1952/Sh2-244).
var catalogSources = []struct{ file, source string }{
	{"messier.csv", "messier"},
	{"ngc.csv", "ngc"},
	{"ic.csv", "ic"},
	{"sh2.csv", "sh2"},
	{"ldn.csv", "ldn"},
}

var (
	catalogCacheMu sync.RWMutex
	catalogCache   = map[string]*Catalog{}
)

// LoadCatalog parses every Siril catalog under dir into one merged Catalog, cached per directory so
// the ~15k-row parse happens only once. A missing individual catalog file is skipped; a missing
// directory yields an empty (non-nil) Catalog.
func LoadCatalog(dir string) (*Catalog, error) {
	catalogCacheMu.RLock()
	cached, ok := catalogCache[dir]
	catalogCacheMu.RUnlock()
	if ok {
		return cached, nil
	}
	c, err := loadCatalog(dir)
	if err != nil {
		return nil, err
	}
	catalogCacheMu.Lock()
	catalogCache[dir] = c
	catalogCacheMu.Unlock()
	return c, nil
}

func loadCatalog(dir string) (*Catalog, error) {
	c := &Catalog{index: map[string]*Record{}}
	for _, src := range catalogSources {
		recs, err := parseCatalogFile(filepath.Join(dir, src.file), src.source)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("load catalog %s: %w", src.file, err)
		}
		for _, r := range recs {
			c.add(r)
		}
	}
	return c, nil
}

// Records returns a copy of every merged record, so callers cannot mutate the cached catalog.
func (c *Catalog) Records() []Record {
	out := make([]Record, len(c.records))
	for i, r := range c.records {
		out[i] = *r
	}
	return out
}

// Lookup returns the merged record matching name (by primary name or any alias), case- and
// punctuation-insensitive.
func (c *Catalog) Lookup(name string) (Record, bool) {
	if r, ok := c.index[normalize(name)]; ok {
		return *r, true
	}
	return Record{}, false
}

// add inserts r, merging it into an existing record when its name or any alias is already known.
func (c *Catalog) add(r *Record) {
	for _, k := range normalizedKeys(r) {
		if existing, ok := c.index[k]; ok {
			mergeInto(existing, r)
			c.register(existing) // the merge may have added new alias keys
			return
		}
	}
	c.records = append(c.records, r)
	c.register(r)
}

// register points every normalized name/alias key of r at r (without clobbering an earlier owner).
func (c *Catalog) register(r *Record) {
	for _, k := range normalizedKeys(r) {
		if _, exists := c.index[k]; !exists {
			c.index[k] = r
		}
	}
}

// mergeInto folds src into dst: fill missing photometry and absorb src's name and aliases.
func mergeInto(dst, src *Record) {
	if !dst.HasMag && src.HasMag {
		dst.MagV, dst.HasMag = src.MagV, true
	}
	if !dst.HasDiameter && src.HasDiameter {
		dst.DiameterArcmin, dst.HasDiameter = src.DiameterArcmin, true
	}
	dst.Aliases = appendAlias(dst.Aliases, dst.Name, src.Name)
	for _, a := range src.Aliases {
		dst.Aliases = appendAlias(dst.Aliases, dst.Name, a)
	}
}

func normalizedKeys(r *Record) []string {
	keys := make([]string, 0, len(r.Aliases)+1)
	if k := normalize(r.Name); k != "" {
		keys = append(keys, k)
	}
	for _, a := range r.Aliases {
		if k := normalize(a); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}

// appendAlias adds candidate to aliases unless it is empty, duplicates an existing alias, or repeats
// the primary name.
func appendAlias(aliases []string, primaryName, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	key := normalize(candidate)
	if key == "" || key == normalize(primaryName) {
		return aliases
	}
	for _, a := range aliases {
		if normalize(a) == key {
			return aliases
		}
	}
	return append(aliases, candidate)
}

// parseCatalogFile reads one catalog file with header-driven column mapping (the files vary: 6 columns
// for messier/ngc/ic, 4 for sh2, 3 for ldn, with some empty diameter/mag cells).
func parseCatalogFile(path, source string) ([]*Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseCatalog(f, source, path)
}

// parseCatalog reads catalog rows from r; name identifies the source in error messages. It is the
// shared core behind both the on-disk loader (parseCatalogFile) and the embedded-snapshot loader.
func parseCatalog(r io.Reader, source, name string) ([]*Record, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(rows) < 2 {
		return nil, nil
	}

	cols := headerIndex(rows[0])
	iName, okName := cols["name"]
	iRA, okRA := cols["ra"]
	iDec, okDec := cols["dec"]
	if !okName || !okRA || !okDec {
		return nil, fmt.Errorf("catalog %s missing name/ra/dec header", name)
	}
	iDiam, hasDiam := cols["diameter"]
	iMag, hasMag := cols["mag"]
	iAlias, hasAlias := cols["alias"]

	out := make([]*Record, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if r := parseRow(row, source, iName, iRA, iDec, iDiam, iMag, iAlias, hasDiam, hasMag, hasAlias); r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func parseRow(row []string, source string, iName, iRA, iDec, iDiam, iMag, iAlias int, hasDiam, hasMag, hasAlias bool) *Record {
	if iName >= len(row) || iRA >= len(row) || iDec >= len(row) {
		return nil
	}
	name := strings.TrimSpace(row[iName])
	ra, errRA := strconv.ParseFloat(strings.TrimSpace(row[iRA]), 64)
	dec, errDec := strconv.ParseFloat(strings.TrimSpace(row[iDec]), 64)
	if name == "" || errRA != nil || errDec != nil {
		return nil
	}
	rec := &Record{Name: name, RADeg: ra, DecDeg: dec, Source: source}
	if hasDiam && iDiam < len(row) {
		if d, err := strconv.ParseFloat(strings.TrimSpace(row[iDiam]), 64); err == nil {
			rec.DiameterArcmin, rec.HasDiameter = d, true
		}
	}
	if hasMag && iMag < len(row) {
		if m, err := strconv.ParseFloat(strings.TrimSpace(row[iMag]), 64); err == nil {
			rec.MagV, rec.HasMag = m, true
		}
	}
	if hasAlias && iAlias < len(row) {
		for _, a := range strings.Split(row[iAlias], "/") {
			if a = strings.TrimSpace(a); a != "" {
				rec.Aliases = append(rec.Aliases, a)
			}
		}
	}
	return rec
}

func headerIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}
