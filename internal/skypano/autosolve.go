package skypano

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

// autosolve.go solves a panel WITHOUT trusting the phone's compass.
//
// Measured against seven solved panels of a real session, the two halves of a phone's pointing are
// not equally trustworthy. The altitude, which comes from the gravity vector, was right every time —
// within about a degree. The compass azimuth was not: two panels came back 5 and 15 degrees out, and
// the four aimed near the zenith were wrong by 166 degrees, near enough a full reversal. Apple's
// heading describes the device, and once a phone is tilted back far enough to photograph overhead
// that stops describing where the rear camera looks at all.
//
// So azimuth is swept and everything else is kept. One unknown over a full circle is a cheap search,
// and the verification already distinguishes a real solve from chance by an order of magnitude — on
// p04 the correct azimuth returned 690 matched stars against 84 for the recorded one.

// AzimuthSweep is the step, in degrees, between trial azimuths. The field is tens of degrees across,
// so a coarse step still puts the true sky inside the frame for the quads to find.
const AzimuthSweep = 30

// ConclusiveInliers is the matched-star count above which a trial is accepted without finishing the
// sweep. A correct solve is not a close call — the real ones land between 575 and 739 stars where
// chance is about 90 — so there is nothing to gain by continuing, and a sweep that runs to the end
// costs twelve solves instead of one or two.
const ConclusiveInliers = 300

// AutoSolve solves a panel by sweeping azimuth, keeping the best-verified result. It returns the
// camera, the solution and the azimuth that produced it.
//
// cat and det must reach comparable DEPTH — see SolveQuads, where that requirement is the difference
// between solving and returning noise.
func AutoSolve(f pointing.Frame, orientation, w, h int, focal35mm float64, rowsBottomUp bool,
	catFor func(raDeg, decDeg float64) [][3]float64, det []Detection, o QuadSolveOptions,
) (Camera, Solution, float64, bool) {
	var best Camera
	var bestSol Solution
	bestAz, found := 0.0, false

	for _, delta := range sweepOrder() {
		trial := f
		trial.AzDeg = math.Mod(f.AzDeg+delta+360, 360)

		prior, ok := PriorCamera(trial, orientation, w, h, rowsBottomUp)
		if !ok {
			return Camera{}, Solution{}, 0, false
		}
		prior.F = FocalPixels(focal35mm, w, h)
		ra, dec, _, ok := trial.Equatorial()
		if !ok {
			return Camera{}, Solution{}, 0, false
		}
		cam, sol, ok := SolveQuads(prior, catFor(ra, dec), det, o)
		if !ok || sol.Matches <= bestSol.Matches {
			continue
		}
		best, bestSol, bestAz, found = cam, sol, trial.AzDeg, true
		if sol.Matches >= ConclusiveInliers {
			return best, bestSol, bestAz, true
		}
	}
	return best, bestSol, bestAz, found
}

// sweepOrder tries the recorded azimuth first, then its opposite, then fans outwards. The reversal
// comes second because that is where the measured error actually sat: four panels aimed near the
// zenith were out by about 166 degrees, the recorded heading having stopped describing the camera
// once the phone was tilted that far back.
func sweepOrder() []float64 {
	out := []float64{0, 180}
	for d := AzimuthSweep; d < 180; d += AzimuthSweep {
		out = append(out, float64(d), float64(-d), float64(180+d), float64(180-d))
	}
	return out
}
