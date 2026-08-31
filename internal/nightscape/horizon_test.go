package nightscape

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// TestBuildSkyAlpha_ShatteredMaskFallsBackToAllSky covers the backstop for the failure that made
// the pilot run come out flat grey: thresholding a noisy starfield fragments the candidate mask, the
// flood fill from the seed row reaches almost none of it, and the mask declares the frame entirely
// foreground — so the composite showed one unregistered frame instead of a 31-frame stack.
//
// Smoothing before the threshold now prevents that on a full-size frame, which is why this uses a
// small one, where the pre-blur is skipped. The primary defence against a pure-sky frame is not
// here at all: a percentile threshold always splits an image roughly in half, so no pixel statistic
// can tell "no ground" from "some ground". Only the camera's tilt can, and that is horizonInFrame.
func TestBuildSkyAlpha_ShatteredMaskFallsBackToAllSky(t *testing.T) {
	const w, h = 60, 60
	im := fits.NewImage(w, h, 3)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < w*h; i++ {
		v := float32(0.0075 + rng.NormFloat64()*0.0020) // pure sky noise, no structure at all
		for c := 0; c < 3; c++ {
			im.Pix[c][i] = v
		}
	}

	alpha, note := buildSkyAlpha(im, 45, 15, 12, nil)

	assert.NotEmpty(t, note, "the fallback must say why, not act silently")
	for i, a := range alpha {
		require.InDelta(t, 1, a, 1e-6, "pixel %d should be sky", i)
	}
}

func TestHalfFOVDeg(t *testing.T) {
	tests := []struct {
		name  string
		meta  rawmeta.Meta
		want  float64
		wanOK bool
	}{
		{
			name: "iphone 16 pro main camera",
			// 24 mm equivalent on a 4:3 frame: the long edge spans 71.6 degrees, so half is 35.8.
			meta:  rawmeta.Meta{FocalLength35mm: 24, Width: 4032, Height: 3024},
			want:  35.8,
			wanOK: true,
		},
		{
			name:  "portrait-stored frame gives the same answer",
			meta:  rawmeta.Meta{FocalLength35mm: 24, Width: 3024, Height: 4032},
			want:  35.8,
			wanOK: true,
		},
		{
			name:  "a 50 mm normal lens",
			meta:  rawmeta.Meta{FocalLength35mm: 50, Width: 6000, Height: 4000},
			want:  19.8,
			wanOK: true,
		},
		{
			name:  "no focal length, no answer",
			meta:  rawmeta.Meta{Width: 4032, Height: 3024},
			wanOK: false,
		},
		{
			name:  "no dimensions, no answer",
			meta:  rawmeta.Meta{FocalLength35mm: 24},
			wanOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := halfFOVDeg(tt.meta)

			require.Equal(t, tt.wanOK, ok)
			if !tt.wanOK {
				return
			}
			assert.InDelta(t, tt.want, got, 0.1)
		})
	}
}

// TestHorizonReach documents the decision for each pointing of the real session, since this is what
// decides whether a panel keeps the foreground path at all.
func TestHorizonReach(t *testing.T) {
	half, ok := halfFOVDeg(rawmeta.Meta{FocalLength35mm: 24, Width: 4032, Height: 3024})
	require.True(t, ok)

	tests := []struct {
		name        string
		altDeg      float64
		wantInFrame bool
	}{
		{"p02, aimed at the sea horizon", 16.2, true},
		// p01 and p03 put their lower edge 3.5 degrees above the horizon: sky, with at most an
		// obstruction in a corner, which the stack's dark floor removes far better than a mask that
		// has to guess where the ground is.
		{"p01, low south-west", 39.3, false},
		{"p03, low south-west", 39.5, false},
		{"p04, half way up the north-east", 49.8, false},
		{"p05/p06, high north-east", 63.1, false},
		{"p07, near the zenith", 74.1, false},
		{"p08, the zenith panel that broke the pilot", 75.6, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantInFrame, tt.altDeg-half <= horizonMarginDeg,
				"lower edge sits at %.1f degrees", math.Round((tt.altDeg-half)*10)/10)
		})
	}
}
