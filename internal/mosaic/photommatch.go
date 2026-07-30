package mosaic

import (
	"context"
	"errors"
	"fmt"
)

// PairFit is one overlap measurement between two panels (indices into the panel slice).
type PairFit struct {
	A, B          int
	Samples       int
	Gain, Offset  float64 // v_B ≈ Gain·v_A + Offset over the overlap
	ResidualSigma float64
	// OffsetOnly marks a reference-channel fit whose contamination guard tripped — gain fixed
	// to 1. Refit channels reuse the solved gains, so the flag stays false there.
	OffsetOnly bool
}

// PhotomSolution holds per-panel corrections mapping each panel onto the anchor's scale:
// corrected = v·Gain[i] + Offset[i] (indices follow the panel slice order). Gains are solved once
// (reference channel) and shared across channels; offsets are refit per channel (sky pedestals are
// filter-dependent).
type PhotomSolution struct {
	Gain, Offset []float64
	Anchor       int
	Pairs        []PairFit
	Warnings     []string
}

// FitPhotometry measures every overlapping pair on the reference channel and solves the panel
// graph by anchor-fixed weighted least squares (log-gains first, then offsets given gains) —
// loop closure beats chained propagation (see solve.go for the derivation). mode:
// "gain_offset" | "offset" | "off". Panels disconnected from the anchor get sky-leveled with a
// warning instead of failing the fit.
func FitPhotometry(ctx context.Context, panels []PanelImage, canvas CanvasSpec, mode string, workers int) (*PhotomSolution, error) {
	n := len(panels)
	if n == 0 {
		return nil, errors.New("mosaic: no panels to fit photometry over")
	}
	if err := checkPanels(panels); err != nil {
		return nil, err
	}
	gainAllowed := false
	switch mode {
	case "off":
		return identitySolution(n), nil
	case "gain_offset":
		gainAllowed = true
	case "offset":
	default:
		return nil, fmt.Errorf("mosaic: unknown photometric mode %q", mode)
	}
	pairs, err := measurePairs(ctx, panels, canvas, candidatePairs(panels, canvas), workers,
		func(a, b int, va, vb []float32) PairFit { return fitPair(a, b, va, vb, gainAllowed) })
	if err != nil {
		return nil, err
	}
	sol := identitySolution(n)
	sol.Anchor = anchorPanel(n, pairs)
	sol.Pairs = pairs
	solvable := anchorComponentPairs(n, sol.Anchor, pairs)
	gains, ok := solveGains(n, sol.Anchor, solvable)
	if !ok {
		sol.Warnings = append(sol.Warnings, "gain solve degenerate — keeping unit gains")
	}
	sol.Gain = gains
	offsets, ok := solveOffsets(n, sol.Anchor, solvable, gains)
	if !ok {
		sol.Warnings = append(sol.Warnings, "offset solve degenerate — keeping zero offsets")
	}
	sol.Offset = offsets
	levelIslands(panels, sol)
	return sol, nil
}

// RefitOffsets keeps sol's gains and re-solves only the offsets against another channel's panel
// images (same panel order). Returns a new solution; sol is untouched. A panel whose overlap
// vanishes on this channel is re-leveled by sky like any island.
func (sol *PhotomSolution) RefitOffsets(ctx context.Context, panels []PanelImage, canvas CanvasSpec, workers int) (*PhotomSolution, error) {
	n := len(panels)
	if len(sol.Gain) != n || len(sol.Offset) != n {
		return nil, fmt.Errorf("mosaic: photometric solution covers %d panel(s), channel has %d", len(sol.Gain), n)
	}
	if err := checkPanels(panels); err != nil {
		return nil, err
	}
	cands := make([]pairKey, 0, len(sol.Pairs))
	for _, pr := range sol.Pairs {
		cands = append(cands, pairKey{pr.A, pr.B})
	}
	pairs, err := measurePairs(ctx, panels, canvas, cands, workers,
		func(a, b int, va, vb []float32) PairFit { return fitPairOffset(a, b, va, vb, pairGainOf(sol, a, b)) })
	if err != nil {
		return nil, err
	}
	out := &PhotomSolution{
		Gain:   append([]float64(nil), sol.Gain...),
		Anchor: sol.Anchor,
		Pairs:  pairs,
	}
	offsets, ok := solveOffsets(n, out.Anchor, anchorComponentPairs(n, out.Anchor, pairs), out.Gain)
	if !ok {
		out.Warnings = append(out.Warnings, "offset refit degenerate — keeping zero offsets")
	}
	out.Offset = offsets
	levelIslands(panels, out)
	return out, nil
}

// pairGainOf is the pair-level gain implied by the solved per-panel gains: from
// g_A = g_B·G_AB (solve.go), G_AB = g_A/g_B.
func pairGainOf(sol *PhotomSolution, a, b int) float64 {
	if a < 0 || b < 0 || a >= len(sol.Gain) || b >= len(sol.Gain) || sol.Gain[b] == 0 {
		return 1
	}
	return sol.Gain[a] / sol.Gain[b]
}

// identitySolution is the no-op correction (mode "off", or the seed the solvers fill in).
func identitySolution(n int) *PhotomSolution {
	sol := &PhotomSolution{Gain: make([]float64, n), Offset: make([]float64, n)}
	for i := range sol.Gain {
		sol.Gain[i] = 1
	}
	return sol
}
