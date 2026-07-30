package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mosaic"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Plate-solving one mosaic panel: the aligned broadband reference master is solved ONCE per panel
// (per-channel solves would carry independent errors into channels alignChannels just co-registered,
// and narrowband panels often cannot solve at all) with a ladder of position hints.

const mosaicSolveName = "_mosaic_solve"

// solvePanelWCS plate-solves baseName (an aligned master base name inside dir, no extension) and
// returns its TAN solution. hints are "RA,Dec" position seeds tried in order (most specific first);
// an empty hint uses whatever opts.Solve already carries.
func solvePanelWCS(ctx context.Context, opts Options, dir, baseName string, hints []string) (fits.WCS, error) {
	var lastErr error
	for _, hint := range dedupHints(hints) {
		s := opts.Solve
		if hint != "" {
			s.Coords = hint
		}
		if _, err := opts.Runner.Run(ctx, dir, siril.ParityProbeScript(baseName, mosaicSolveName, s), nil); err != nil {
			lastErr = err
			continue
		}
		probePath := filepath.Join(dir, mosaicSolveName+".fits")
		f, err := fits.Open(probePath)
		_ = os.Remove(probePath)
		if err != nil {
			lastErr = err
			continue
		}
		if w, ok := fits.ParseWCS(f.Header); ok {
			return w, nil
		}
		lastErr = fmt.Errorf("solved file carries no usable TAN WCS")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no position hint available")
	}
	return fits.WCS{}, fmt.Errorf("plate-solve failed: %w", lastErr)
}

// panelSolveHints builds the hint ladder for one panel: the matched plan tile center, the panel's
// own header centroid, then the run-level seed already in opts.Solve.Coords.
func panelSolveHints(opts Options, panel mosaic.Panel) []string {
	var hints []string
	if panel.PlanTile != nil {
		hints = append(hints, coordHint(panel.PlanTile.RA, panel.PlanTile.Dec))
	}
	if panel.HasCenter {
		hints = append(hints, coordHint(panel.RA, panel.Dec))
	}
	hints = append(hints, "") // opts.Solve.Coords (the run-level seed), or Siril's own metadata
	return hints
}

func coordHint(ra, dec float64) string {
	return fmt.Sprintf("%.5f,%.5f", ra, dec)
}

func dedupHints(hints []string) []string {
	seen := map[string]bool{}
	out := hints[:0]
	for _, h := range hints {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out
}

// mosaicPlanFOVDeg estimates the tile field width from a plan: consecutive serpentine tiles are one
// step = FOV·(1−overlap) apart by construction. No plan (or a degenerate one) → 1° — a sane default
// for the clustering threshold, which only needs the right order of magnitude.
func mosaicPlanFOVDeg(plan *mosaic.Plan) float64 {
	if plan == nil || len(plan.Tiles) < 2 || plan.OverlapFrac >= 1 {
		return 1.0
	}
	var first, second *mosaic.Tile
	for i := range plan.Tiles {
		switch plan.Tiles[i].Order {
		case 1:
			first = &plan.Tiles[i]
		case 2:
			second = &plan.Tiles[i]
		}
	}
	if first == nil || second == nil {
		return 1.0
	}
	sep := astro.AngularSeparation(first.RA, first.Dec, second.RA, second.Dec)
	if sep <= 0 {
		return 1.0
	}
	return sep / (1 - plan.OverlapFrac)
}

// mosaicWorkers bounds the assembler's row-band parallelism: ASTRO_MAX_WORKERS when set (the same
// env the rest of the engine honors), else half the CPUs, in [1,8].
func mosaicWorkers() int {
	if v := os.Getenv("ASTRO_MAX_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.NumCPU() / 2
	if n < 1 {
		return 1
	}
	if n > 8 {
		return 8
	}
	return n
}
