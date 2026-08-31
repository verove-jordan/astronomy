// Package platesolve turns one image file into a plate solution: where the telescope was really
// pointing when it was taken.
//
// The processing pipeline already solves images, but through annotate's run-directory machinery
// (cached per run, tied to a master frame, unexported). Centring a telescope needs the same thing
// stripped down — solve THIS file, now, tell me where it points — so this is a thin wrapper over the
// same Siril command rather than a refactor of a working path.
package platesolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// solveTimeout bounds one solve. Centring runs these in a loop between exposures, so a solve that
// has not converged in a minute is a failed solve, not a slow one.
const solveTimeout = 60 * time.Second

// Solver solves single files.
type Solver struct {
	runner *siril.Runner
	opts   siril.SolveOptions
}

// New builds a solver over an existing Siril runner. opts carry the catalogue configuration, so an
// installation with the local Gaia extract solves offline exactly as the pipeline does.
func New(runner *siril.Runner, opts siril.SolveOptions) *Solver {
	return &Solver{runner: runner, opts: opts}
}

// Result is one solution.
type Result struct {
	WCS           fits.WCS
	RADeg         float64 // the frame CENTRE, which is what a centring loop steers
	DecDeg        float64
	ScaleArcsecPx float64
}

// Hint narrows the search. A blind solve over the whole sky is minutes of work; with the mount's own
// idea of where it points — even a degree off — it is seconds.
type Hint struct {
	RADeg   float64
	DecDeg  float64
	HasHint bool
	FocalMM float64
	PixelUm float64
}

// Solve plate-solves one FITS file and reports where its centre points.
//
// Siril solves IN PLACE, so the file is copied into a scratch directory first — a centring loop must
// never rewrite the frames it was handed, and on a capture run those frames are the night's data.
// openSolved finds the file Siril actually wrote for `save <name>`.
//
// Which extension that is comes from `setext` in the script header — it says `fits`, so Siril writes
// `<name>.fits`. This used to look only for `<name>.fit` and therefore never found it: every solve
// failed with "solved file unreadable: no such file or directory" AFTER Siril had already logged
// "Siril solve succeeded", which reads like a broken image rather than a missing suffix. Accept both
// spellings so the reader does not silently depend on a preference set three files away.
func openSolved(dir, name string) (*fits.File, error) {
	var firstErr error
	for _, ext := range []string{".fits", ".fit"} {
		f, err := fits.Open(filepath.Join(dir, name+ext))
		if err == nil {
			return f, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

func (s *Solver) Solve(ctx context.Context, path string, hint Hint) (Result, error) {
	if s == nil || s.runner == nil {
		return Result{}, fmt.Errorf("no Siril runner configured")
	}
	work, err := os.MkdirTemp("", "astro-solve-*")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(work) }()

	const inName, outName = "solve_in", "solve_out"
	if err := copyFile(path, filepath.Join(work, inName+".fit")); err != nil {
		return Result{}, err
	}

	opts := s.opts
	if hint.HasHint {
		opts.Coords = fmt.Sprintf("%.6f,%.6f", hint.RADeg, hint.DecDeg)
	}
	if hint.FocalMM > 0 {
		opts.FocalMM = hint.FocalMM
	}
	if hint.PixelUm > 0 {
		opts.PixelUm = hint.PixelUm
	}

	runCtx, cancel := context.WithTimeout(ctx, solveTimeout)
	defer cancel()
	script := siril.ParityProbeScript(inName, outName, opts)
	if _, err := s.runner.Run(runCtx, work, script, nil); err != nil {
		return Result{}, fmt.Errorf("plate solve failed: %w", err)
	}

	solved, err := openSolved(work, outName)
	if err != nil {
		return Result{}, fmt.Errorf("solved file unreadable: %w", err)
	}
	wcs, ok := fits.ParseWCS(solved.Header)
	if !ok {
		return Result{}, fmt.Errorf("the solve produced no WCS — the field may be too sparse")
	}
	w, h := solved.Dimensions()
	// The centre of the frame is what a centring loop steers, not the reference pixel: the two are
	// the same only when the solver happened to put CRPIX in the middle.
	raDeg, decDeg := wcs.PixToSky(float64(w)/2, float64(h)/2)
	return Result{
		WCS: wcs, RADeg: raDeg, DecDeg: decDeg,
		ScaleArcsecPx: wcs.ScaleArcsecPerPix(),
	}, nil
}

// Available reports whether solving is possible at all, so a caller can degrade to "point by hand"
// rather than failing mid-session.
func (s *Solver) Available(ctx context.Context) error {
	if s == nil || s.runner == nil {
		return fmt.Errorf("no Siril runner configured")
	}
	return s.runner.Available(ctx)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.ToLower(src), ".fit") &&
		!strings.HasSuffix(strings.ToLower(src), ".fits") {
		return fmt.Errorf("plate solve needs a FITS file, got %s", filepath.Ext(src))
	}
	return os.WriteFile(dst, data, 0o644)
}
