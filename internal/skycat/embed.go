package skycat

import (
	"embed"
	"path"
	"sync"
)

// embeddedCatalogueFS carries a snapshot of Siril's deep-sky catalogues (Messier/NGC/IC/Sh2/LDN — the
// same CSVs the macOS Siril app bundles) compiled into the binary. It is the fallback the tonight
// planner and the name→coordinate resolver use when no readable on-disk Siril catalogue is found:
// inside the Docker engine image the Linux distro Siril ships a different (legacy semicolon .txt)
// catalogue format the CSV parser can't read, and a host may have no Siril at all. See
// catalogue/README.md for provenance.
//
//go:embed catalogue/*.csv
var embeddedCatalogueFS embed.FS

const embeddedCatalogueDir = "catalogue"

var (
	embeddedOnce sync.Once
	embedded     *Catalog
)

// loadEmbeddedCatalog parses the embedded CSV snapshot once and caches it for the process lifetime.
func loadEmbeddedCatalog() *Catalog {
	embeddedOnce.Do(func() {
		c := &Catalog{index: map[string]*Record{}}
		for _, src := range catalogSources {
			f, err := embeddedCatalogueFS.Open(path.Join(embeddedCatalogueDir, src.file))
			if err != nil {
				continue // stay resilient if a file is ever dropped from the snapshot
			}
			recs, perr := parseCatalog(f, src.source, src.file)
			f.Close()
			if perr != nil {
				continue
			}
			for _, r := range recs {
				c.add(r)
			}
		}
		embedded = c
	})
	return embedded
}

// Load returns the deep-sky catalogue under dir, falling back to the embedded CSV snapshot when dir
// has no readable catalogue (missing, empty, or a Siril build whose catalogue is in a format/location
// this parser doesn't read — e.g. the Linux distro's legacy .txt). Unlike LoadCatalog it never errors
// and yields an empty catalogue only if the embedded snapshot itself is unavailable, so features that
// must always have a catalogue (the tonight planner, name→coord resolution) should use it.
func Load(dir string) *Catalog {
	if c, err := LoadCatalog(dir); err == nil && len(c.records) > 0 {
		return c
	}
	return loadEmbeddedCatalog()
}
