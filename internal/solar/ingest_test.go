package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func scansWithScores(scores ...float64) []frameScan {
	out := make([]frameScan, len(scores))
	for i, s := range scores {
		out[i] = frameScan{index: i, score: s, ok: s > 0, limb: Limb{CX: 100, CY: 100, R: 50}}
	}
	return out
}

// TestSelectFrames covers frame selection: which frames survive, how many, and in what order.
func TestSelectFrames(t *testing.T) {
	t.Run("keeps the sharpest, in capture order", func(t *testing.T) {
		got := selectFrames(scansWithScores(0.1, 0.9, 0.2, 0.8, 0.3), 40, 100)
		require.Len(t, got, 2)
		assert.Equal(t, 1, got[0].index)
		assert.Equal(t, 3, got[1].index, "output must be in capture order, not score order")
	})

	t.Run("the hard cap wins over the percentage", func(t *testing.T) {
		scores := make([]float64, 1000)
		for i := range scores {
			scores[i] = float64(i%97) / 97
		}
		got := selectFrames(scansWithScores(scores...), 90, 50)
		assert.Len(t, got, 50)
	})

	t.Run("frames with no limb are never selected", func(t *testing.T) {
		got := selectFrames(scansWithScores(0.5, 0, 0.4, 0, 0.3), 100, 100)
		require.Len(t, got, 3)
		for _, f := range got {
			assert.True(t, f.ok)
		}
	})

	t.Run("at least one frame survives a tiny clip", func(t *testing.T) {
		assert.Len(t, selectFrames(scansWithScores(0.5), 10, 100), 1)
	})

	t.Run("nothing usable yields nothing", func(t *testing.T) {
		assert.Empty(t, selectFrames(scansWithScores(0, 0, 0), 100, 100))
	})
}

// TestCropFor checks the crop the whole clip is materialised through: it must follow the Sun across
// the frame, keep room for prominences, and stay inside the source.
func TestCropFor(t *testing.T) {
	res := scanResult{
		limb: Limb{CX: 200, CY: 200, R: 100},
		frames: []frameScan{
			{ok: true, limb: Limb{CX: 180, CY: 200, R: 100}},
			{ok: true, limb: Limb{CX: 220, CY: 210, R: 100}},
		},
	}
	c := cropFor(res, 2, 960, 960, 0.2)

	t.Run("covers every frame's disc plus the margin", func(t *testing.T) {
		// In scan pixels the union spans x 60..340; at scale 2 that is 120..680.
		assert.LessOrEqual(t, c.x, 120)
		assert.GreaterOrEqual(t, c.x+c.w, 680)
	})
	t.Run("offsets and sizes are even", func(t *testing.T) {
		// Odd values would shift a subsampled-chroma frame by half a sample.
		assert.Zero(t, c.x%2)
		assert.Zero(t, c.y%2)
		assert.Zero(t, c.w%2)
		assert.Zero(t, c.h%2)
	})
	t.Run("stays inside the source frame", func(t *testing.T) {
		assert.GreaterOrEqual(t, c.x, 0)
		assert.LessOrEqual(t, c.x+c.w, 960)
		assert.LessOrEqual(t, c.y+c.h, 960)
	})
	t.Run("falls back to the whole frame with no geometry", func(t *testing.T) {
		full := cropFor(scanResult{}, 1, 640, 480, 0.2)
		assert.True(t, full.covers(640, 480))
	})
}

// TestBuildAndApplyLUT covers the photometric mapping.
func TestBuildAndApplyLUT(t *testing.T) {
	t.Run("maps a scaled frame onto its reference", func(t *testing.T) {
		src := []float64{0.05, 0.10, 0.20, 0.40}
		ref := []float64{0.10, 0.20, 0.40, 0.80} // exactly twice as bright
		lut := buildLUT(src, ref)
		require.NotNil(t, lut)
		p := []float32{0.05, 0.10, 0.20, 0.40}
		applyLUT(p, lut)
		for i := range p {
			assert.InDelta(t, ref[i], float64(p[i]), 1e-6)
		}
	})

	t.Run("is monotone, so no banding is introduced", func(t *testing.T) {
		lut := buildLUT([]float64{0.05, 0.1, 0.3, 0.6}, []float64{0.08, 0.2, 0.35, 0.9})
		require.NotNil(t, lut)
		p := make([]float32, 200)
		for i := range p {
			p[i] = float32(i) / 200
		}
		applyLUT(p, lut)
		for i := 1; i < len(p); i++ {
			require.GreaterOrEqual(t, p[i], p[i-1], "the mapping must never fold back")
		}
	})

	t.Run("extrapolates past the probes instead of flattening them", func(t *testing.T) {
		// A saturated core is brighter than any probe; clamping it onto the last knot would erase
		// the very highlights the clipping gates look for.
		lut := buildLUT([]float64{0.1, 0.2, 0.3, 0.4}, []float64{0.2, 0.4, 0.6, 0.8})
		require.NotNil(t, lut)
		p := []float32{0.9}
		applyLUT(p, lut)
		assert.Greater(t, float64(p[0]), 0.8)
	})

	t.Run("an identity mapping is skipped", func(t *testing.T) {
		v := []float64{0.1, 0.2, 0.3, 0.4}
		assert.Nil(t, buildLUT(v, v), "rewriting a frame for no change is pure cost")
	})

	t.Run("degenerate curves are refused", func(t *testing.T) {
		flat := []float64{0.2, 0.2, 0.2, 0.2}
		assert.Nil(t, buildLUT(flat, []float64{0.1, 0.2, 0.3, 0.4}))
	})
}

// TestDetectBand checks the channel choice. Through a 0.6 Å Hα etalon the green and blue channels
// hold nothing but noise, so averaging them in would dilute the signal threefold.
func TestDetectBand(t *testing.T) {
	tests := []struct {
		name       string
		red, green float32
		want       Band
	}{
		{"deep red through an etalon", 0.8, 0.05, BandHAlpha},
		{"neutral white light", 0.6, 0.55, BandWhiteLight},
		{"mildly warm", 0.6, 0.4, BandWhiteLight},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, g := fits.NewImage(64, 64, 1), fits.NewImage(64, 64, 1)
			for i := range r.Pix[0] {
				r.Pix[0][i], g.Pix[0][i] = tt.red, tt.green
			}
			assert.Equal(t, tt.want, detectBand(r, g))
		})
	}
}

// TestCropAround checks the per-frame crop that pre-centres stills on their own disc.
func TestCropAround(t *testing.T) {
	s := defaultSun()
	s.w, s.h, s.cx, s.cy, s.r = 900, 800, 600.5, 300.5, 150
	im := drawSun(s)

	t.Run("centres the disc", func(t *testing.T) {
		out := cropAround(im, s.cx, s.cy, 400)
		require.Equal(t, 400, out.W)
		l, ok := FitLimb(out)
		require.True(t, ok)
		assert.InDelta(t, 200, l.CX, 1.0, "the disc lands at the crop centre")
		assert.InDelta(t, 200, l.CY, 1.0)
		assert.InDelta(t, s.r, l.R, 0.6, "cropping must not disturb the geometry")
	})

	t.Run("a window running off the frame is padded, not shifted", func(t *testing.T) {
		out := cropAround(im, 40, 40, 400)
		require.Equal(t, 400, out.W)
		require.Equal(t, 400, out.H)
		assert.False(t, math.IsNaN(float64(out.Pix[0][0])))
	})
}

// TestScaleTo covers the reduced-resolution sizing used by the scanning pass.
func TestScaleTo(t *testing.T) {
	tests := []struct {
		name           string
		w, h, maxEdge  int
		wantW, wantH   int
		wantScaleDelta float64
	}{
		{"already small enough", 800, 600, 960, 800, 600, 1},
		{"landscape downscale", 3840, 2160, 960, 960, 540, 4},
		{"portrait downscale", 2160, 3840, 960, 540, 960, 4},
		{"odd dimensions are made even", 801, 601, 960, 800, 600, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, scale := scaleTo(tt.w, tt.h, tt.maxEdge)
			assert.Equal(t, tt.wantW, w)
			assert.Equal(t, tt.wantH, h)
			assert.InDelta(t, tt.wantScaleDelta, scale, 0.01)
			assert.Zero(t, w%2)
			assert.Zero(t, h%2)
		})
	}
}

// TestDecodeGray16BE pins the byte order of the raw stream ffmpeg hands us. Getting it backwards
// would not crash — it would quietly produce noise that still stacks.
func TestDecodeGray16BE(t *testing.T) {
	dst := make([]float32, 3)
	decodeGray16BE([]byte{0x00, 0x00, 0xFF, 0xFF, 0x80, 0x00}, dst)
	assert.InDelta(t, 0.0, float64(dst[0]), 1e-6)
	assert.InDelta(t, 1.0, float64(dst[1]), 1e-6)
	assert.InDelta(t, 0.5, float64(dst[2]), 1e-3)
}
