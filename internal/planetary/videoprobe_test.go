package planetary

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPngPixFmtFor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"unknown stays default", "", ""},
		{"8-bit gray stays default", "gray", ""},
		{"8-bit yuv stays default", "yuv420p", ""},
		{"8-bit 4:1:0 subsampling is NOT 10-bit", "yuv410p", ""},
		{"10-bit gray", "gray10le", "gray16be"},
		{"12-bit gray", "gray12be", "gray16be"},
		{"16-bit gray", "gray16le", "gray16be"},
		{"16-bit gray+alpha", "ya16be", "gray16be"},
		{"10-bit yuv", "yuv420p10le", "rgb48be"},
		{"12-bit yuv", "yuv422p12le", "rgb48be"},
		{"semi-planar 10-bit", "p010le", "rgb48be"},
		{"48-bit rgb", "rgb48be", "rgb48be"},
		{"64-bit rgba", "rgba64le", "rgb48be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pngPixFmtFor(tt.in))
		})
	}
}

func TestVideoPixFmt_SoftFailsOnMissingBinary(t *testing.T) {
	got := videoPixFmt(context.Background(), "/definitely/not/a/real/ffprobe", "whatever.mp4")
	assert.Empty(t, got, "a missing/broken ffprobe keeps the historical 8-bit extraction")
}

func TestFfprobeBinFor(t *testing.T) {
	t.Setenv("FFPROBE_BIN", "")
	assert.Equal(t, "ffprobe", ffprobeBinFor(""), "no ffmpeg configured → PATH lookup")
	assert.Equal(t, "ffprobe", ffprobeBinFor("ffmpeg"), "bare name → PATH lookup")
	assert.Equal(t, "/opt/ffmpeg/bin/ffprobe", ffprobeBinFor("/opt/ffmpeg/bin/ffmpeg"), "sibling of the configured ffmpeg")
	t.Setenv("FFPROBE_BIN", "/custom/probe")
	assert.Equal(t, "/custom/probe", ffprobeBinFor("/opt/ffmpeg/bin/ffmpeg"), "env override wins")
}
