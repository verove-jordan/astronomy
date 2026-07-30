package pec

import (
	"errors"
	"math"
)

// Separating the worm from everything else that moved the star.
//
// Drift and periodic error are fitted TOGETHER, in one design matrix, rather than by detrending and
// then fitting the residual. Sequential detrending looks equivalent and is not: unless the run covers
// a whole number of worm cycles, a slice of the worm's own sine is indistinguishable from a slope, so
// the line eats part of the periodic signal and the amplitude comes out low. Fitting jointly lets the
// two terms compete on the same evidence.
//
// The quadratic term is there for differential refraction, which curves the drift over an hour and
// would otherwise be absorbed by the harmonics as a spurious very-low-frequency component.

// ErrTooFewSamples is returned when there is not enough data to fit anything trustworthy.
var ErrTooFewSamples = errors.New("too few samples to fit a worm curve")

// Harmonic is one component of the worm's error.
type Harmonic struct {
	K               int
	AmplitudeArcsec float64 // semi-amplitude
	PhaseRad        float64
	SigmaArcsec     float64
	// Significant is false for harmonics indistinguishable from noise. Those are left out of the
	// curve: writing them would replay the night's seeing into the mount for ever.
	Significant bool
}

// Fit is the decomposition of a run into drift plus worm harmonics.
type Fit struct {
	DriftArcsecPerSec float64
	Harmonics         []Harmonic
	// Curve is the periodic position error at bin EDGES: length Bins+1, last element equal to the
	// first. Edges, not centres, because that is what the correction differences.
	Curve             []float64
	ResidualRMSArcsec float64
	Samples           int
}

// FitCurve fits drift and worm harmonics jointly.
func FitCurve(samples []Sample, g Geometry) (*Fit, error) {
	if !g.valid() {
		return nil, errors.New("invalid PEC geometry")
	}
	kMax := maxHarmonic(g.Bins)
	cols := 3 + 2*kMax
	if len(samples) < cols*2 {
		return nil, ErrTooFewSamples
	}

	normal, rhs := accumulateNormalEquations(samples, g, kMax, cols)
	coef, ok := solveSymmetric(normal, rhs)
	if !ok {
		return nil, errors.New("the worm fit is singular — the run may cover too little phase")
	}

	fit := &Fit{DriftArcsecPerSec: coef[1], Samples: len(samples)}
	fit.ResidualRMSArcsec = residualRMS(samples, g, kMax, coef)
	fit.Harmonics = harmonicsFrom(coef, kMax, fit.ResidualRMSArcsec, len(samples))
	fit.Curve = curveAtEdges(coef, fit.Harmonics, g)
	return fit, nil
}

// designRow builds one row of the design matrix: [1, t, t², cos/sin of each harmonic].
func designRow(s Sample, g Geometry, kMax, cols int) []float64 {
	row := make([]float64, cols)
	row[0], row[1], row[2] = 1, s.TimeSec, s.TimeSec*s.TimeSec
	phase := 2 * math.Pi * wrapPhase(s.PhaseBins, g.Bins) / float64(g.Bins)
	for k := 1; k <= kMax; k++ {
		row[3+2*(k-1)] = math.Cos(float64(k) * phase)
		row[4+2*(k-1)] = math.Sin(float64(k) * phase)
	}
	return row
}

func accumulateNormalEquations(samples []Sample, g Geometry, kMax, cols int) ([][]float64, []float64) {
	normal := make([][]float64, cols)
	for i := range normal {
		normal[i] = make([]float64, cols)
	}
	rhs := make([]float64, cols)
	for _, s := range samples {
		row := designRow(s, g, kMax, cols)
		for i := 0; i < cols; i++ {
			rhs[i] += row[i] * s.Arcsec
			for j := i; j < cols; j++ {
				normal[i][j] += row[i] * row[j]
			}
		}
	}
	for i := 0; i < cols; i++ {
		for j := 0; j < i; j++ {
			normal[i][j] = normal[j][i]
		}
	}
	return normal, rhs
}

func residualRMS(samples []Sample, g Geometry, kMax int, coef []float64) float64 {
	var sum float64
	for _, s := range samples {
		row := designRow(s, g, kMax, len(coef))
		model := 0.0
		for i, c := range coef {
			model += c * row[i]
		}
		d := s.Arcsec - model
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// harmonicsFrom converts the fitted coefficients into amplitudes, and decides which are real.
//
// The uncertainty is the standard result for a near-orthogonal basis — residual RMS × sqrt(2/N) —
// which holds when phase is well covered, and phase coverage is exactly what a multi-cycle run
// guarantees. It is an approximation, and it is used only to decide significance, never reported as
// a precise error bar.
func harmonicsFrom(coef []float64, kMax int, residRMS float64, n int) []Harmonic {
	sigma := residRMS * math.Sqrt(2/float64(n))
	out := make([]Harmonic, 0, kMax)
	for k := 1; k <= kMax; k++ {
		a, b := coef[3+2*(k-1)], coef[4+2*(k-1)]
		amp := math.Hypot(a, b)
		out = append(out, Harmonic{
			K:               k,
			AmplitudeArcsec: amp,
			PhaseRad:        math.Atan2(b, a),
			SigmaArcsec:     sigma,
			Significant:     amp > 2*sigma,
		})
	}
	return out
}

// curveAtEdges evaluates the periodic part at every bin boundary.
//
// The constant and drift terms are deliberately excluded: PEC cannot correct drift — the table is
// replayed identically every revolution — and trying would write a net rate into the mount that made
// it walk a little further every cycle.
func curveAtEdges(coef []float64, harmonics []Harmonic, g Geometry) []float64 {
	curve := make([]float64, g.Bins+1)
	for b := 0; b <= g.Bins; b++ {
		phase := 2 * math.Pi * float64(b) / float64(g.Bins)
		var v float64
		for _, h := range harmonics {
			if !h.Significant {
				continue
			}
			a, bb := coef[3+2*(h.K-1)], coef[4+2*(h.K-1)]
			v += a*math.Cos(float64(h.K)*phase) + bb*math.Sin(float64(h.K)*phase)
		}
		curve[b] = v
	}
	return curve
}

// solveSymmetric solves A·x = b by Gaussian elimination with partial pivoting. The system is tiny
// (a few dozen columns), so nothing cleverer is warranted.
func solveSymmetric(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	m := make([][]float64, n)
	for i := range m {
		m[i] = append(append([]float64(nil), a[i]...), b[i])
	}
	for col := 0; col < n; col++ {
		pivot := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r][col]) > math.Abs(m[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(m[pivot][col]) < 1e-12 {
			return nil, false
		}
		m[col], m[pivot] = m[pivot], m[col]
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := m[r][col] / m[col][col]
			for c := col; c <= n; c++ {
				m[r][c] -= f * m[col][c]
			}
		}
	}
	x := make([]float64, n)
	for i := 0; i < n; i++ {
		x[i] = m[i][n] / m[i][i]
	}
	return x, true
}
