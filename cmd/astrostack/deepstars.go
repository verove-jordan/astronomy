package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/deepstars/gen"
)

// runDeepstarsData rebuilds the embedded deep star catalogue (internal/deepstars) from the HYG
// database — network at generation time only; the regenerated .csv.gz is committed so the engine
// annotates star names fully offline.
func runDeepstarsData(args []string) error {
	fs := flag.NewFlagSet("deepstars-data", flag.ContinueOnError)
	mag := fs.Float64("mag", gen.DefaultMagLimit, "faintest star magnitude to include")
	out := fs.String("out", gen.DefaultOutPath, "output .csv.gz path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return gen.Generate(context.Background(), gen.Options{MagLimit: *mag, OutPath: *out})
}

// runDeepstarsATHYG builds the DEEP star catalogue (ATHYG v3.2, ~2.5 million stars down to about
// magnitude 13) into the library so the annotation can name the field stars a stack actually
// resolves — the embedded extract stops at magnitude 9, which leaves a typical eleventh-magnitude
// detection anonymous. The file is downloaded and converted, never committed: at ~130 MB it lives
// beside the Gaia plate-solve catalogues under <library>/catalogues.
func runDeepstarsATHYG(args []string) error {
	cfg := config.Load()
	fs := flag.NewFlagSet("deepstars-athyg", flag.ContinueOnError)
	out := fs.String("out", cfg.DeepStarCat, "output .bin path")
	mag := fs.Float64("mag", 0, "drop stars fainter than this magnitude (0 = keep all)")
	src := fs.String("src", "", "comma-separated source .csv.gz URLs or local paths (default: the pinned ATHYG v3.2 release)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var sources []string
	if *src != "" {
		for _, s := range strings.Split(*src, ",") {
			if s = strings.TrimSpace(s); s != "" {
				sources = append(sources, s)
			}
		}
	}
	if *out == "" {
		*out = filepath.Join("library", "catalogues", deepstars.DefaultCatalogFile)
	}
	return deepstars.Build(context.Background(), deepstars.BuildOptions{
		Sources:  sources,
		OutPath:  *out,
		MagLimit: *mag,
		Log:      func(s string) { fmt.Println("deepstars-athyg:", s) },
	})
}
