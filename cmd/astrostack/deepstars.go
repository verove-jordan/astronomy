package main

import (
	"context"
	"flag"

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
