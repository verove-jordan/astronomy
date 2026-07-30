package mosaic

// Photometric graph solve.
//
// Each panel i carries a correction corrected_i(v) = g_i·v + b_i mapping it onto the anchor's
// scale (g_anchor = 1, b_anchor = 0). A measured pair (A,B) models the overlap as
// v_B ≈ G_AB·v_A + O_AB; for every sky point in the overlap the corrected values must agree:
//
//	g_A·v_A + b_A == g_B·v_B + b_B == (g_B·G_AB)·v_A + (g_B·O_AB + b_B)   for all v_A
//
// Matching the v_A coefficient and the constant term separately gives one pair of linear
// relations per overlap:
//
//	g_A = g_B·G_AB       →  α_A − α_B = ln G_AB    with α = ln g    (gains, log domain)
//	b_A − b_B = g_B·O_AB                                            (offsets, given gains)
//
// Both are anchored weighted least-squares systems over the overlap graph, weight ln(1+Samples);
// OffsetOnly pairs constrain offsets but never gains. Solving the whole graph at once means every
// loop's disagreement is averaged away — chained propagation would accumulate a drifted edge into
// one visible seam at the closing panel.

import "math"

// solveGains solves the anchored least squares for the per-panel gains on the log scale.
// ok=false only when the pinned normal system is still singular (then unit gains come back).
func solveGains(n, anchor int, pairs []PairFit) ([]float64, bool) {
	m, rhs := newSystem(n - 1)
	for _, pr := range pairs {
		if pr.OffsetOnly || pr.Gain <= 0 {
			continue
		}
		addPairEq(m, rhs, anchor, pr.A, pr.B, math.Log(pr.Gain), pairWeight(pr))
	}
	pinFree(m)
	alpha, ok := solveLinear(m, rhs)
	gains := make([]float64, n)
	for i := range gains {
		gains[i] = 1
	}
	if !ok {
		return gains, false
	}
	for i := 0; i < n; i++ {
		if i != anchor {
			gains[i] = math.Exp(alpha[unknownIdx(i, anchor)])
		}
	}
	return gains, true
}

// solveOffsets solves the anchored least squares for the per-panel offsets, gains held fixed.
func solveOffsets(n, anchor int, pairs []PairFit, gains []float64) ([]float64, bool) {
	m, rhs := newSystem(n - 1)
	for _, pr := range pairs {
		addPairEq(m, rhs, anchor, pr.A, pr.B, gains[pr.B]*pr.Offset, pairWeight(pr))
	}
	pinFree(m)
	b, ok := solveLinear(m, rhs)
	offsets := make([]float64, n)
	if !ok {
		return offsets, false
	}
	for i := 0; i < n; i++ {
		if i != anchor {
			offsets[i] = b[unknownIdx(i, anchor)]
		}
	}
	return offsets, true
}

// pairWeight weighs an overlap equation by how much data measured it.
func pairWeight(pr PairFit) float64 { return math.Log(1 + float64(pr.Samples)) }

// unknownIdx maps a panel index to its unknown index once the anchor is eliminated.
func unknownIdx(i, anchor int) int {
	if i < anchor {
		return i
	}
	return i - 1
}

// newSystem allocates a u×u normal matrix and its right-hand side.
func newSystem(u int) ([][]float64, []float64) {
	if u < 0 {
		u = 0
	}
	m := make([][]float64, u)
	for i := range m {
		m[i] = make([]float64, u)
	}
	return m, make([]float64, u)
}

// addPairEq folds one x_A − x_B = r equation (weight w) into the anchored normal equations; the
// anchor unknown is fixed at 0, so its terms fold into the right-hand side.
func addPairEq(m [][]float64, rhs []float64, anchor, a, b int, r, w float64) {
	if a == b {
		return
	}
	switch {
	case a == anchor: // −x_B = r
		ub := unknownIdx(b, anchor)
		m[ub][ub] += w
		rhs[ub] -= w * r
	case b == anchor: // x_A = r
		ua := unknownIdx(a, anchor)
		m[ua][ua] += w
		rhs[ua] += w * r
	default:
		ua, ub := unknownIdx(a, anchor), unknownIdx(b, anchor)
		m[ua][ua] += w
		m[ub][ub] += w
		m[ua][ub] -= w
		m[ub][ua] -= w
		rhs[ua] += w * r
		rhs[ub] -= w * r
	}
}

// pinFree pins unknowns no equation touches (all-zero diagonal) so the system stays solvable;
// those panels keep the neutral correction.
func pinFree(m [][]float64) {
	for k := range m {
		if m[k][k] == 0 {
			m[k][k] = 1
		}
	}
}

// solveLinear solves a small dense system by Gaussian elimination with partial pivoting, working
// on copies so the caller's system is untouched. ok=false on a singular pivot.
func solveLinear(m [][]float64, rhs []float64) ([]float64, bool) {
	u := len(m)
	x := make([]float64, u)
	if u == 0 {
		return x, true
	}
	a := make([][]float64, u)
	for i := range a {
		a[i] = append([]float64(nil), m[i]...)
	}
	b := append([]float64(nil), rhs...)
	for col := 0; col < u; col++ {
		piv := col
		for r := col + 1; r < u; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if math.Abs(a[piv][col]) < 1e-12 {
			return nil, false
		}
		a[col], a[piv] = a[piv], a[col]
		b[col], b[piv] = b[piv], b[col]
		for r := col + 1; r < u; r++ {
			f := a[r][col] / a[col][col]
			if f == 0 {
				continue
			}
			for c := col; c < u; c++ {
				a[r][c] -= f * a[col][c]
			}
			b[r] -= f * b[col]
		}
	}
	for r := u - 1; r >= 0; r-- {
		s := b[r]
		for c := r + 1; c < u; c++ {
			s -= a[r][c] * x[c]
		}
		x[r] = s / a[r][r]
	}
	return x, true
}
