package platesolve

import (
	"context"
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Solving simulated frames, by reading the answer off the back of the card.
//
// Siril cannot plate-solve a frame the simulator drew: the bundled catalogue stops near magnitude 9,
// which leaves ten or twenty stars in a one-degree field — below the matching threshold — and adding
// invented stars makes it worse, because fabricated stars appear in no reference catalogue and poison
// the match outright. simsolve_test.go records both failures as an executable note.
//
// The consequence is not small. Everything built on plate solving — centring, tracking measurement,
// and polar alignment from the camera — could otherwise only ever be exercised on a real clear night,
// with real hardware, at the one moment when a bug costs the most. So the simulated camera writes down
// where it was pointing (see sim.truthCards) and this reads it back, producing exactly the solution
// Siril would have produced if it could.
//
// It is development scaffolding and it is fenced as such: it exists only behind ASTRO_SIM_SOLVER, it
// only ever answers for frames carrying the simulator's own cards, and it refuses anything else rather
// than inventing a solution for a real photograph.

// SimSolver answers from the simulator's truth cards instead of from the sky.
type SimSolver struct{}

// NewSimSolver builds the simulated solver. It takes no configuration: everything it needs is in the
// frame, which is the point — a solution derived from config rather than from the file would drift out
// of step with what the camera actually drew.
func NewSimSolver() *SimSolver { return &SimSolver{} }

// Available always succeeds: there is nothing to install.
func (s *SimSolver) Available(context.Context) error { return nil }

// Solve reads the truth cards and builds the plate solution they describe.
func (s *SimSolver) Solve(_ context.Context, path string, _ Hint) (Result, error) {
	f, err := fits.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("simulated solve: %w", err)
	}
	ra, okRA := f.Header.Float("SIMRA")
	dec, okDec := f.Header.Float("SIMDEC")
	scale, okScale := f.Header.Float("SIMSCALE")
	if !okRA || !okDec || !okScale {
		return Result{}, fmt.Errorf(
			"simulated solve: %s carries no simulator truth cards — it is not a simulated frame", path)
	}
	pa := 0.0
	if v, ok := f.Header.Float("SIMPA"); ok {
		pa = v
	}

	w, h := f.Dimensions()
	if w <= 0 || h <= 0 {
		return Result{}, fmt.Errorf("simulated solve: %s has no image", path)
	}

	wcs, ok := fits.NewTanWCS(ra, dec, float64(w)/2+1, float64(h)/2+1, cdMatrix(scale, pa))
	if !ok {
		return Result{}, fmt.Errorf("simulated solve: plate scale %g is degenerate", scale)
	}
	return Result{WCS: wcs, RADeg: ra, DecDeg: dec, ScaleArcsecPx: scale}, nil
}

// cdMatrix builds the plate solution's transform: a scale, a rotation by the camera's position angle,
// and the flip along the first axis that every sky image has, since right ascension increases to the
// left of north.
func cdMatrix(scaleArcsecPx, paDeg float64) [2][2]float64 {
	s := scaleArcsecPx / 3600
	sin, cos := math.Sincos(paDeg * math.Pi / 180)
	return [2][2]float64{
		{-s * cos, s * sin},
		{s * sin, s * cos},
	}
}
