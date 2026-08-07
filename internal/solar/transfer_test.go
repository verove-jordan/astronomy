package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLinearizeHLG pins the inverse HLG transfer against the values BT.2100 defines. Every clip in
// a real iPhone solar session arrives HLG-encoded, and the host ffmpeg cannot invert it, so this
// function standing between the container and every downstream stage is not optional.
func TestLinearizeHLG(t *testing.T) {
	tests := []struct {
		name string
		in   float32
		want float64
	}{
		{"black", 0, 0},
		{"quarter signal", 0.25, 0.25 * 0.25 / 3},
		{"branch point", 0.5, 0.5 * 0.5 / 3},
		{"peak", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := []float32{tt.in}
			linearizeHLG(p)
			assert.InDelta(t, tt.want, float64(p[0]), 1e-5)
		})
	}

	t.Run("continuous at the branch point", func(t *testing.T) {
		p := []float32{0.4999, 0.5001}
		linearizeHLG(p)
		assert.InDelta(t, float64(p[0]), float64(p[1]), 1e-4, "the two branches must meet")
	})

	t.Run("monotone and strongly compressive in the shadows", func(t *testing.T) {
		p := make([]float32, 101)
		for i := range p {
			p[i] = float32(i) / 100
		}
		linearizeHLG(p)
		for i := 1; i < len(p); i++ {
			require.Greater(t, p[i], p[i-1], "HLG inversion must be monotone")
		}
		// Half signal is a twelfth of peak light: this is the whole reason stacking the raw signal
		// would be wrong — mid-tones are compressed by an order of magnitude.
		assert.InDelta(t, 1.0/12, float64(p[50]), 0.02)
	})
}

// TestLinearizePQ pins the inverse PQ transfer at its defined endpoints.
func TestLinearizePQ(t *testing.T) {
	p := []float32{0, 0.5, 1}
	linearizePQ(p)
	assert.InDelta(t, 0.0, float64(p[0]), 1e-6)
	assert.InDelta(t, 1.0, float64(p[2]), 1e-4)
	assert.Greater(t, p[1], float32(0))
	assert.Less(t, p[1], float32(1))
}

// TestExpandRange checks the limited-range expansion at each bit depth. The bounds are depth
// specific on purpose: ffmpeg widens a 10-bit sample to 16 bits by bit replication, so the 10-bit
// 64..940 window lands at 64/1023..940/1023 once normalised — not at the 8-bit 16/255..235/255
// everyone quotes.
func TestExpandRange(t *testing.T) {
	tests := []struct {
		name     string
		depth    int
		full     bool
		in, want float32
	}{
		{"10-bit black", 10, false, 64.0 / 1023, 0},
		{"10-bit white", 10, false, 940.0 / 1023, 1},
		{"8-bit black", 8, false, 16.0 / 255, 0},
		{"8-bit white", 8, false, 235.0 / 255, 1},
		{"below black clamps", 10, false, 0, 0},
		{"full range untouched", 10, true, 0.3, 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := []float32{tt.in}
			expandRange(p, tt.depth, tt.full)
			assert.InDelta(t, float64(tt.want), float64(p[0]), 1e-4)
		})
	}

	t.Run("superwhite is preserved, not clipped", func(t *testing.T) {
		// A genuinely clipped disc must still read as clipped downstream; folding everything above
		// nominal white to exactly 1.0 would hide it from the triage saturation gate.
		p := []float32{1.0}
		expandRange(p, 10, false)
		assert.Greater(t, p[0], float32(1.0))
	})
}

// TestBitDepthOf covers the pixel formats a phone and an astro camera actually produce.
func TestBitDepthOf(t *testing.T) {
	tests := []struct {
		pixFmt string
		want   int
	}{
		{"yuv420p", 8},
		{"yuv420p10le", 10}, // iPhone HEVC HDR
		{"yuv422p10le", 10}, // iPhone ProRes
		{"yuv444p12le", 12},
		{"gray16be", 16},
		{"rgb24", 8},
		{"", 0},
	}
	for _, tt := range tests {
		t.Run(tt.pixFmt, func(t *testing.T) {
			assert.Equal(t, tt.want, bitDepthOf(tt.pixFmt))
		})
	}
}

// TestParseProbe covers the ffprobe output shape, including the HLG and rotation tags that a real
// iPhone clip carries and that the ingest path has to react to.
func TestParseProbe(t *testing.T) {
	out := `width=3840
height=2160
r_frame_rate=120/1
pix_fmt=yuv420p10le
color_range=tv
color_transfer=arib-std-b67
codec_name=hevc
duration=25.018333
rotation=-90
duration=25.018333
TAG:creation_time=2026-07-30T13:07:20.000000Z
`
	v := parseProbe(out)
	assert.Equal(t, 3840, v.Width)
	assert.Equal(t, 2160, v.Height)
	assert.InDelta(t, 120.0, v.FPS, 1e-9)
	assert.InDelta(t, 25.018333, v.DurationSec, 1e-6)
	assert.Equal(t, 3002, v.Frames, "frame count is derived from rate x duration")
	assert.Equal(t, 10, v.BitDepth)
	assert.Equal(t, -90, v.Rotation)
	assert.True(t, v.IsHDR(), "arib-std-b67 is HLG and must be flagged for inversion")
	assert.False(t, v.FullRange())
	assert.Greater(t, v.CreatedMs, int64(0))

	t.Run("rotation swaps the display dimensions", func(t *testing.T) {
		w, h := displayDims(v)
		assert.Equal(t, 2160, w)
		assert.Equal(t, 3840, h)
	})

	t.Run("missing color_range defaults to limited", func(t *testing.T) {
		assert.False(t, parseProbe("width=1920\npix_fmt=yuv420p\n").FullRange())
	})
}

// TestLinearize_EndToEnd runs a limited-range HLG sample through the whole path.
func TestLinearize_EndToEnd(t *testing.T) {
	info := VideoInfo{PixFmt: "yuv420p10le", BitDepth: 10, Transfer: "arib-std-b67", ColorRange: "tv"}
	p := []float32{64.0 / 1023, 940.0 / 1023}
	Linearize(p, info)
	assert.InDelta(t, 0.0, float64(p[0]), 1e-4, "nominal black must land at zero light")
	assert.InDelta(t, 1.0, float64(p[1]), 1e-3, "nominal white must land at peak light")
	assert.False(t, math.IsNaN(float64(p[0])))
}
