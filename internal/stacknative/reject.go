package stacknative

import (
	"math"
	"sort"
)

// The per-pixel outlier tests. Each takes one pixel's samples across the sequence and marks which of
// them survive; the combination method then reduces the survivors to a single value.
//
// These are OUR implementations of the published algorithms, not bindings to Siril's. Where an
// algorithm exists in both engines the results agree statistically (the parity test compares median,
// sigma and rejected fraction on the same frames) but not bit for bit — the iteration order and the
// scale estimator differ in the last digits. The engine that ran is recorded in run.json.

// scratch holds the reusable buffers one pixel's rejection needs, so a full-frame pass allocates
// nothing per pixel.
type scratch struct {
	sorted []float64
	work   []float64
	keep   []bool
}

func newScratch(n int) *scratch {
	return &scratch{sorted: make([]float64, 0, n), work: make([]float64, 0, n), keep: make([]bool, n)}
}

// resetKeep marks every sample as surviving and returns the mask. The slice is the scratch's OWN
// buffer — reused by the next pixel, which is the point (a full-frame pass allocates nothing) but
// means a caller that needs two masks at once must copy the first.
func (s *scratch) resetKeep(n int) []bool {
	if cap(s.keep) < n {
		s.keep = make([]bool, n)
	}
	s.keep = s.keep[:n]
	for i := range s.keep {
		s.keep[i] = true
	}
	return s.keep
}

// median returns the median of v, reordering the scratch copy rather than v itself.
func median(v []float64, s *scratch) float64 {
	s.sorted = append(s.sorted[:0], v...)
	sort.Float64s(s.sorted)
	return medianSorted(s.sorted)
}

func medianSorted(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return sorted[n/2]
	}
	return 0.5 * (sorted[n/2-1] + sorted[n/2])
}

// mad returns the median absolute deviation about m, scaled to a standard-deviation equivalent for
// a normal distribution (×1.4826).
func mad(v []float64, m float64, s *scratch) float64 {
	s.work = s.work[:0]
	for _, x := range v {
		s.work = append(s.work, math.Abs(x-m))
	}
	sort.Float64s(s.work)
	return 1.4826 * medianSorted(s.work)
}

// meanStd returns the mean and (population) standard deviation of the kept samples.
func meanStd(v []float64, keep []bool) (mean, std float64, n int) {
	var sum float64
	for i, x := range v {
		if keep[i] {
			sum += x
			n++
		}
	}
	if n == 0 {
		return 0, 0, 0
	}
	mean = sum / float64(n)
	var ss float64
	for i, x := range v {
		if keep[i] {
			d := x - mean
			ss += d * d
		}
	}
	return mean, math.Sqrt(ss / float64(n)), n
}

// rejectNone keeps every sample.
func rejectNone(v []float64, s *scratch) []bool { return s.resetKeep(len(v)) }

// rejectPercentile drops samples whose RELATIVE distance from the median exceeds the given
// fractions — the definition Siril and PixInsight use. It measures no spread at all, which is why it
// is the only honest test on a handful of frames.
func rejectPercentile(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	m := median(v, s)
	if m == 0 {
		return keep
	}
	for i, x := range v {
		switch {
		case x < m && (m-x)/math.Abs(m) > low:
			keep[i] = false
		case x > m && (x-m)/math.Abs(m) > high:
			keep[i] = false
		}
	}
	return keep
}

// rejectSigma is classic iterative kappa-sigma clipping about the MEAN. A very bright outlier
// inflates the sigma it is judged against, which is exactly what winsorization fixes.
func rejectSigma(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	for iter := 0; iter < maxClipIters; iter++ {
		mean, std, n := meanStd(v, keep)
		if n < 3 || std <= 0 {
			return keep
		}
		dropped := false
		for i, x := range v {
			if keep[i] && (x < mean-low*std || x > mean+high*std) {
				keep[i], dropped = false, true
			}
		}
		if !dropped {
			return keep
		}
	}
	return keep
}

// rejectMedianSigma clips about the MEDIAN instead of the mean, so the outliers being hunted cannot
// drag the centre toward themselves.
func rejectMedianSigma(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	for iter := 0; iter < maxClipIters; iter++ {
		s.work = s.work[:0]
		for i, x := range v {
			if keep[i] {
				s.work = append(s.work, x)
			}
		}
		if len(s.work) < 3 {
			return keep
		}
		kept := append([]float64(nil), s.work...)
		m := median(kept, s)
		_, std, _ := meanStd(v, keep)
		if std <= 0 {
			return keep
		}
		dropped := false
		for i, x := range v {
			if keep[i] && (x < m-low*std || x > m+high*std) {
				keep[i], dropped = false, true
			}
		}
		if !dropped {
			return keep
		}
	}
	return keep
}

// rejectWinsorized is Huber winsorization: before the spread is measured, the extreme samples are
// PULLED IN to the edge of the distribution rather than deleted, so the sigma is estimated from data
// the outliers cannot inflate. The best all-round compromise, and the engine's mid-range default.
func rejectWinsorized(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	if len(v) < 3 {
		return keep
	}
	m := median(v, s)
	sigma := mad(v, m, s)
	if sigma <= 0 {
		return keep
	}
	// Iterate the winsorized sigma to convergence (Huber's k = 1.5).
	for iter := 0; iter < maxClipIters; iter++ {
		s.work = s.work[:0]
		lo, hi := m-1.5*sigma, m+1.5*sigma
		for _, x := range v {
			s.work = append(s.work, math.Min(math.Max(x, lo), hi))
		}
		var sum float64
		for _, x := range s.work {
			sum += x
		}
		mean := sum / float64(len(s.work))
		var ss float64
		for _, x := range s.work {
			d := x - mean
			ss += d * d
		}
		next := 1.134 * math.Sqrt(ss/float64(len(s.work)))
		if next <= 0 {
			break
		}
		if math.Abs(next-sigma)/sigma < winsorTolerance {
			sigma = next
			break
		}
		sigma = next
	}
	for i, x := range v {
		if x < m-low*sigma || x > m+high*sigma {
			keep[i] = false
		}
	}
	return keep
}

// rejectMAD clips at k times the median absolute deviation — a spread estimate computed WITHOUT the
// outliers rather than from them. Blunter than winsorization, and very hard to fool.
func rejectMAD(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	m := median(v, s)
	sigma := mad(v, m, s)
	if sigma <= 0 {
		return keep
	}
	for i, x := range v {
		if x < m-low*sigma || x > m+high*sigma {
			keep[i] = false
		}
	}
	return keep
}

// rejectLinearFit fits a robust straight line through the samples AS A FUNCTION OF FRAME INDEX and
// rejects the ones that fall off it. A sky level that changed smoothly during the session (a rising
// moon, drifting light pollution) is then modelled instead of being treated as noise — which no
// centre-and-spread test can do.
func rejectLinearFit(v []float64, low, high float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	n := len(v)
	if n < 4 {
		return keep
	}
	for iter := 0; iter < maxClipIters; iter++ {
		a, b, ok := fitLine(v, keep)
		if !ok {
			return keep
		}
		// Robust residual scale from the kept samples.
		s.work = s.work[:0]
		for i, x := range v {
			if keep[i] {
				s.work = append(s.work, x-(a*float64(i)+b))
			}
		}
		if len(s.work) < 3 {
			return keep
		}
		res := append([]float64(nil), s.work...)
		rm := median(res, s)
		sigma := mad(res, rm, s)
		if sigma <= 0 {
			return keep
		}
		dropped := false
		for i, x := range v {
			if !keep[i] {
				continue
			}
			r := x - (a*float64(i) + b)
			if r < rm-low*sigma || r > rm+high*sigma {
				keep[i], dropped = false, true
			}
		}
		if !dropped {
			return keep
		}
	}
	return keep
}

// fitLine is an ordinary least-squares fit of the kept samples against their frame index.
func fitLine(v []float64, keep []bool) (a, b float64, ok bool) {
	var sx, sy, sxx, sxy float64
	var n float64
	for i, y := range v {
		if !keep[i] {
			continue
		}
		x := float64(i)
		sx, sy, sxx, sxy, n = sx+x, sy+y, sxx+x*x, sxy+x*y, n+1
	}
	if n < 3 {
		return 0, 0, false
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0, false
	}
	a = (n*sxy - sx*sy) / den
	b = (sy - a*sx) / n
	return a, b, true
}

// rejectGESD is the Generalized Extreme Studentized Deviate test: repeatedly ask whether the most
// extreme REMAINING sample is a genuine outlier, up to outlierFrac of the stack, at significance
// alpha. Statistically the most rigorous option, and the engine's default past 50 frames — it
// catches the correlated leftovers (walking noise, trail remnants) a fixed 3σ clip leaves behind.
func rejectGESD(v []float64, outlierFrac, alpha float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	n := len(v)
	maxOut := int(outlierFrac * float64(n))
	if n < 5 || maxOut < 1 {
		return keep
	}
	type candidate struct {
		idx int
		r   float64
	}
	var order []candidate
	work := append([]bool(nil), keep...)
	for k := 0; k < maxOut; k++ {
		mean, std, cnt := meanStd(v, work)
		if cnt < 3 || std <= 0 {
			break
		}
		worst, worstR := -1, 0.0
		for i, x := range v {
			if !work[i] {
				continue
			}
			if r := math.Abs(x-mean) / std; r > worstR {
				worst, worstR = i, r
			}
		}
		if worst < 0 {
			break
		}
		order = append(order, candidate{worst, worstR})
		work[worst] = false
	}
	// The largest k whose test statistic exceeds its critical value decides how many are outliers.
	cut := 0
	for k, c := range order {
		if c.r > gesdCritical(n, k+1, alpha) {
			cut = k + 1
		}
	}
	for i := 0; i < cut; i++ {
		keep[order[i].idx] = false
	}
	return keep
}

// gesdCritical is the GESD critical value λ_i for a sample of n at significance alpha, using the
// Student-t approximation from the standard formulation.
func gesdCritical(n, i int, alpha float64) float64 {
	nn := float64(n)
	ii := float64(i)
	p := 1 - alpha/(2*(nn-ii+1))
	df := nn - ii - 1
	if df <= 0 {
		return math.Inf(1)
	}
	t := studentT(p, df)
	return (nn - ii) * t / math.Sqrt((df+t*t)*(nn-ii+1))
}

// rejectRCR is Robust Chauvenet Rejection: instead of a fixed threshold it asks how many samples a
// stack of THIS size should contain by chance beyond the observed deviation, and rejects the ones
// that fail that expectation. The aggressiveness adapts to the stack depth on its own.
func rejectRCR(v []float64, alpha float64, s *scratch) []bool {
	keep := s.resetKeep(len(v))
	if len(v) < 4 {
		return keep
	}
	for iter := 0; iter < maxClipIters; iter++ {
		s.work = s.work[:0]
		for i, x := range v {
			if keep[i] {
				s.work = append(s.work, x)
			}
		}
		n := len(s.work)
		if n < 4 {
			return keep
		}
		kept := append([]float64(nil), s.work...)
		m := median(kept, s)
		sigma := mad(kept, m, s)
		if sigma <= 0 {
			return keep
		}
		// Chauvenet: reject when the expected number of samples this deviant is below alpha.
		worst, worstD := -1, 0.0
		for i, x := range v {
			if !keep[i] {
				continue
			}
			if d := math.Abs(x-m) / sigma; d > worstD {
				worst, worstD = i, d
			}
		}
		if worst < 0 {
			return keep
		}
		expected := float64(n) * math.Erfc(worstD/math.Sqrt2)
		if expected >= alpha {
			return keep
		}
		keep[worst] = false
	}
	return keep
}

// studentT approximates the inverse Student-t CDF (quantile) for probability p and df degrees of
// freedom via the Cornish-Fisher expansion — accurate to a few 1e-3 over the range GESD uses, which
// is far finer than the rejection decision needs.
func studentT(p, df float64) float64 {
	z := normalQuantile(p)
	z2 := z * z
	g1 := (z2*z + z) / 4
	g2 := (5*z2*z2*z + 16*z2*z + 3*z) / 96
	g3 := (3*z2*z2*z2*z + 19*z2*z2*z + 17*z2*z - 15*z) / 384
	return z + g1/df + g2/(df*df) + g3/(df*df*df)
}

// normalQuantile is the inverse standard-normal CDF (Acklam's rational approximation).
func normalQuantile(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(1)
	}
	a := [6]float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
		1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := [5]float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
		6.680131188771972e+01, -1.328068155288572e+01}
	c := [6]float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
		-2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := [4]float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00,
		3.754408661907416e+00}
	const plow, phigh = 0.02425, 1 - 0.02425
	switch {
	case p < plow:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > phigh:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	default:
		q := p - 0.5
		r := q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	}
}
