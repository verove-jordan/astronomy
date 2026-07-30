package planetary

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// synthTexturedDisk renders a deterministic moon-like test frame: a bright disk carrying
// fine multi-frequency texture (periods ~4–9 px, the band the metric isolates), optionally
// box-blurred (seeing) and with seeded Gaussian noise; black sky outside the disk.
func synthTexturedDisk(w, h, blurR int, noiseSigma float64, seed int64) *fits.Image {
	pix := make([]float32, w*h)
	cx, cy := float64(w)/2, float64(h)/2
	rad := 0.42 * math.Min(float64(w), float64(h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			if dx*dx+dy*dy > rad*rad {
				continue
			}
			fx, fy := float64(x), float64(y)
			tex := 0.06*math.Sin(fx*2*math.Pi/4.3)*math.Cos(fy*2*math.Pi/5.1) +
				0.06*math.Sin((fx+fy)*2*math.Pi/6.7) +
				0.04*math.Cos(fx*2*math.Pi/9.2)*math.Sin(fy*2*math.Pi/8.3)
			pix[y*w+x] = float32(0.5 + tex)
		}
	}
	if blurR > 0 {
		pix = comet.BoxBlur(pix, w, h, blurR)
	}
	if noiseSigma > 0 {
		rng := rand.New(rand.NewSource(seed))
		for i := range pix {
			pix[i] += float32(rng.NormFloat64() * noiseSigma)
		}
	}
	return &fits.Image{W: w, H: h, C: 1, Pix: [][]float32{pix}}
}

// laplacianDiskMetric reproduces the OLD ranking (scale-invariant Laplacian variance over the
// lit disk). Kept in the tests only, to document the failure mode detailSNR fixes: Laplacian
// variance rewards pixel noise as strongly as structure, so a noisy blurred frame outranks a
// clean sharp one.
func laplacianDiskMetric(im *fits.Image) float64 {
	p := im.Pix[0]
	w, h := im.W, im.H
	bg := lowPercentile(p, 0.2)
	pk := lowPercentile(p, 0.999)
	if pk-bg <= 1e-9 {
		return 0
	}
	thr := float32(bg + apDiskFrac*(pk-bg))
	var sum, sum2 float64
	n := 0
	for y := 1; y < h-1; y++ {
		row := y * w
		for x := 1; x < w-1; x++ {
			c := p[row+x]
			if c <= thr {
				continue
			}
			lap := float64(4*c - p[row+x-1] - p[row+x+1] - p[row-w+x] - p[row+w+x])
			sum += lap
			sum2 += lap * lap
			n++
		}
	}
	if n < 100 {
		return 0
	}
	mean := sum / float64(n)
	return (sum2/float64(n) - mean*mean) / ((pk - bg) * (pk - bg))
}

func TestDetailSNR_RanksByBlurAcrossNoise(t *testing.T) {
	const w, h = 256, 256
	blurs := []int{0, 1, 2, 3}
	tests := []struct {
		name  string
		sigma float64
	}{
		{"no noise", 0},
		{"low noise", 0.01},
		{"high noise", 0.03},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prev float64
			for i, r := range blurs {
				score := detailSNR(synthTexturedDisk(w, h, r, tt.sigma, 42))
				require.Greater(t, score, 0.0, "blur %d must keep measurable detail", r)
				if i > 0 {
					assert.Less(t, score, prev, "blur %d must rank below blur %d", r, blurs[i-1])
				}
				prev = score
			}
		})
	}

	// The motivating defect of the old metric: a heavily blurred but noisy frame OUTRANKS a
	// clean sharp one, because Laplacian variance rewards noise. The new metric keeps the
	// correct order on the same pair.
	sharpClean := synthTexturedDisk(w, h, 0, 0, 42)
	blurredNoisy := synthTexturedDisk(w, h, 3, 0.05, 43)
	assert.Greater(t, laplacianDiskMetric(blurredNoisy), laplacianDiskMetric(sharpClean),
		"old metric inverts under noise (the bug this metric replaces)")
	assert.Greater(t, detailSNR(sharpClean), detailSNR(blurredNoisy),
		"detailSNR keeps the true order under noise")
}

func TestDetailSNR_ScaleInvariant(t *testing.T) {
	im := synthTexturedDisk(256, 256, 1, 0.01, 42)
	scaled := &fits.Image{W: im.W, H: im.H, C: 1, Pix: [][]float32{make([]float32, len(im.Pix[0]))}}
	for i, v := range im.Pix[0] {
		scaled.Pix[0][i] = v * 0.25
	}
	a, b := detailSNR(im), detailSNR(scaled)
	require.Greater(t, a, 0.0)
	assert.InEpsilon(t, a, b, 0.02, "scaling every pixel by 0.25 must not change the metric")
}

func TestDetailSNR_ZeroOnFlat(t *testing.T) {
	tests := []struct {
		name  string
		sigma float64
	}{
		{"flat disk", 0},
		{"flat disk with noise", 0.02},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := synthTexturedDisk(256, 256, 0, tt.sigma, 7)
			// Strip the texture: repaint the disk at a uniform level, keep noise if any.
			for i, v := range im.Pix[0] {
				if v > 0.2 {
					im.Pix[0][i] = 0.5
				}
			}
			if tt.sigma > 0 {
				rng := rand.New(rand.NewSource(7))
				for i := range im.Pix[0] {
					im.Pix[0][i] += float32(rng.NormFloat64() * tt.sigma)
				}
			}
			assert.Less(t, detailSNR(im), 2e-5,
				"a structure-free disk must score ~0 even when noisy (noise fully discounted)")
		})
	}
}

func TestNoiseSigmaHF_RecoversKnownSigma(t *testing.T) {
	const w, h = 256, 256
	tests := []struct {
		name  string
		sigma float64
	}{
		{"sigma 0.005", 0.005},
		{"sigma 0.01", 0.01},
		{"sigma 0.02", 0.02},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pix := make([]float32, w*h)
			for i := range pix {
				pix[i] = 0.5
			}
			rng := rand.New(rand.NewSource(11))
			for i := range pix {
				pix[i] += float32(rng.NormFloat64() * tt.sigma)
			}
			box3 := comet.BoxBlur(pix, w, h, 1)
			got := noiseSigmaHF(pix, box3, 0.25)
			assert.InEpsilon(t, tt.sigma, got, 0.15, "MAD estimator must recover the injected sigma")
		})
	}
}
