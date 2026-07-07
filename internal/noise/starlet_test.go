package noise

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStarlet_Reconstruct_RoundTrip(t *testing.T) {
	// Non-square, odd dimensions exercise the reflect boundary and index arithmetic.
	const w, h = 131, 77
	rng := newRNG(7)
	p := make([]float32, w*h)
	for i := range p {
		p[i] = float32(rng.unit())
	}

	cJ, wcoef := Decompose(p, w, h, 5)
	require.Len(t, wcoef, 5)
	out := Reconstruct(cJ, wcoef)
	require.Len(t, out, len(p))

	var maxd float64
	for i := range p {
		if d := math.Abs(float64(out[i] - p[i])); d > maxd {
			maxd = d
		}
	}
	assert.Lessf(t, maxd, 1e-5, "round-trip max|Δ| = %g", maxd)
}

func TestStarlet_SigmaPropagation(t *testing.T) {
	// A unit-variance white-noise field: the per-scale detail std must reproduce starletSigma.
	const n = 512
	rng := newRNG(12345)
	p := make([]float32, n*n)
	for i := range p {
		p[i] = float32(rng.gauss())
	}

	_, wcoef := Decompose(p, n, n, 6)
	require.GreaterOrEqual(t, len(wcoef), 4)
	for j := 0; j <= 3; j++ {
		got := stdCrop(wcoef[j], n, n, 40) // crop the boundary band to match the infinite-field constants
		assert.InEpsilonf(t, starletSigma[j], got, 0.03, "scale %d: got %g want %g", j, got, starletSigma[j])
	}
}

func TestStarlet_Decompose_ConstantPlane(t *testing.T) {
	// A flat plane has no detail at any scale and a smooth equal to itself.
	const w, h = 40, 24
	p := newPlane(w, h, 0.3)
	cJ, wcoef := Decompose(p, w, h, 4)
	for j, wj := range wcoef {
		assert.InDeltaf(t, 0, stdCrop(wj, w, h, 0), 1e-6, "scale %d detail should be flat", j)
	}
	for i := range cJ {
		require.InDelta(t, 0.3, float64(cJ[i]), 1e-5)
	}
}
