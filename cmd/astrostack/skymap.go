package main

import (
	"context"
	"flag"

	"github.com/verove-jordan/astronomy/internal/skymapgen"
)

// runSkymapData builds the compact star + constellation-line dataset the frontend sky map ships. It
// fetches the HYG catalogue and Stellarium constellation figures (network at build time only) and writes
// frontend/src/assets/skymap.json — after which the app renders the sky fully offline.
func runSkymapData(args []string) error {
	fs := flag.NewFlagSet("skymap-data", flag.ContinueOnError)
	mag := fs.Float64("mag", skymapgen.DefaultMagLimit, "faintest star magnitude to include")
	out := fs.String("out", skymapgen.DefaultOutPath, "output JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return skymapgen.Generate(context.Background(), skymapgen.Options{MagLimit: *mag, OutPath: *out})
}
