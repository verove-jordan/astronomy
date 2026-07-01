package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
)

// runLightPollutionAtlas builds the OFFLINE light-pollution atlas from the David Lorenz model and writes it
// into <DataDir>/lightpollution/. Downloaded once; every per-site/finder/map query is then fully offline.
func runLightPollutionAtlas(args []string) error {
	fs := flag.NewFlagSet("lightpollution-atlas", flag.ContinueOnError)
	region := fs.String("region", "france", "coverage preset: france | europe | world (ignored when --bbox is set)")
	bbox := fs.String("bbox", "", "explicit minLat,minLon,maxLat,maxLon (overrides --region)")
	year := fs.Int("year", 0, "djlorenz atlas year (0 = latest built-in default)")
	out := fs.String("out", "", "output directory (default <DataDir>/lightpollution)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	b, err := lightpollution.ResolveBounds(*region, *bbox)
	if err != nil {
		return err
	}
	dir := *out
	if dir == "" {
		dir = filepath.Join(config.Load().DataDir, "lightpollution")
	}

	total := lightpollution.TileCount(b)
	fmt.Fprintf(os.Stderr, "Building light-pollution atlas (David Lorenz model) — %d tiles for %+v\n", total, b)

	cov, err := lightpollution.BuildAtlas(context.Background(), dir, b, *year, nil, func(done, total int) {
		fmt.Fprintf(os.Stderr, "\r  downloaded %d/%d tiles", done, total)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nDone — %d×%d grid, lat %.2f..%.2f lon %.2f..%.2f → %s\n",
		cov.Cols, cov.Rows, cov.MinLat, cov.MaxLat, cov.MinLon, cov.MaxLon, filepath.Join(dir, "atlas.bin"))
	fmt.Fprintln(os.Stderr, "Light pollution model © David Lorenz (https://djlorenz.github.io/astronomy/lp/).")
	return nil
}
