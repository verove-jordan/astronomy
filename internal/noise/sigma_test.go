package noise

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMeasure_KnownSigmaRecovery(t *testing.T) {
	const w, h = 512, 512
	tests := []struct {
		name  string
		sigma float64
	}{
		{"low", 5e-4},
		{"mid", 1.5e-3},
		{"high", 5e-3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rng := newRNG(101)
			p := newPlane(w, h, 0.05)
			addGradient(p, w, h, 0.08)
			scatterStars(p, w, h, 200, rng, 0.01, 0.08, 1.2, 2.0)
			addNoise(p, rng, tt.sigma)

			rep := Measure(monoImage(w, h, p))
			require.Positive(t, rep.Sigma)
			assert.InEpsilonf(t, tt.sigma, rep.Sigma, 0.08, "recovered sigma %g", rep.Sigma)
			assert.Equal(t, ceilDiv(w, tileSize)*ceilDiv(h, tileSize), len(rep.Tiles))
			assert.Equal(t, tileSize, rep.Tile)
		})
	}
}

func TestMeasure_SpatialSplit(t *testing.T) {
	// A vertical split at a tile boundary: left half sigma_a, right half sigma_b. The per-tile
	// medians of each half must track their injected sigma.
	const w, h = 512, 512
	const sigmaA, sigmaB = 1e-3, 3e-3
	rng := newRNG(202)
	p := newPlane(w, h, 0.05)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s := sigmaA
			if x >= w/2 {
				s = sigmaB
			}
			p[y*w+x] += float32(rng.gauss() * s)
		}
	}

	rep := Measure(monoImage(w, h, p))
	var left, right []float64
	for ty := 0; ty < rep.GridH; ty++ {
		for tx := 0; tx < rep.GridW; tx++ {
			v := float64(rep.Tiles[ty*rep.GridW+tx])
			if tx < rep.GridW/2 {
				left = append(left, v)
			} else {
				right = append(right, v)
			}
		}
	}
	assert.InEpsilonf(t, sigmaA, medianOf(left), 0.12, "left median %g", medianOf(left))
	assert.InEpsilonf(t, sigmaB, medianOf(right), 0.12, "right median %g", medianOf(right))
}

func TestMeasure_HotPixelRobustness(t *testing.T) {
	// 1% salt must not blow up the robust sigma (MAD is insensitive to a small fraction of outliers).
	const w, h = 384, 384
	const sigma = 1.5e-3
	rng := newRNG(303)
	p := newPlane(w, h, 0.05)
	addNoise(p, rng, sigma)
	for i := 0; i < w*h/100; i++ { // 1% hot pixels
		p[int(rng.unit()*float64(w*h))] = 0.9
	}

	rep := Measure(monoImage(w, h, p))
	require.Positive(t, rep.Sigma)
	assert.Lessf(t, rep.Sigma, 1.5*sigma, "sigma blew up to %g", rep.Sigma)
	assert.Greaterf(t, rep.Sigma, 0.6*sigma, "sigma collapsed to %g", rep.Sigma)
}

func TestMeasure_Degenerate(t *testing.T) {
	assert.Equal(t, tileSize, Measure(nil).Tile)
	assert.Zero(t, Measure(nil).Sigma)
	// A constant plane has zero noise and must not panic or divide by zero.
	rep := Measure(monoImage(16, 16, newPlane(16, 16, 0.2)))
	assert.Zero(t, rep.Sigma)
	assert.Zero(t, rep.SNR)
}
