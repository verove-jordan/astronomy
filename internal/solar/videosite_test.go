package solar

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseISO6709(t *testing.T) {
	tests := []struct {
		name             string
		in               string
		wantLat, wantLon float64
		wantElev         float64
		wantOK           bool
	}{
		{"iPhone point, as written on the 12 Aug 2026 clips", "+47.2783-002.4948/", 47.2783, -2.4948, 0, true},
		{"with altitude", "+47.2783-002.4948+0020.000/", 47.2783, -2.4948, 20, true},
		{"southern and eastern", "-33.8688+151.2093/", -33.8688, 151.2093, 0, true},
		{"no trailing solidus", "+47.2783-002.4948", 47.2783, -2.4948, 0, true},
		{"empty", "", 0, 0, 0, false},
		{"only one coordinate", "+47.2783/", 0, 0, 0, false},
		{"latitude off the Earth", "+97.0000-002.4948/", 0, 0, 0, false},
		{"degrees-minutes form is refused rather than guessed", "+4716.7-00229.7/", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lon, elev, ok := parseISO6709(tt.in)
			require.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			assert.InDelta(t, tt.wantLat, lat, 1e-6)
			assert.InDelta(t, tt.wantLon, lon, 1e-6)
			assert.InDelta(t, tt.wantElev, elev, 1e-6)
		})
	}
}

func TestParseProbe_CarriesTheSiteAndTheClock(t *testing.T) {
	out := `width=3840
height=2160
r_frame_rate=30/1
pix_fmt=yuv422p10le
color_transfer=arib-std-b67
codec_name=prores
rotation=-90
duration=1049.450000
TAG:creation_time=2026-08-12T18:11:49.000000Z
TAG:com.apple.quicktime.location.ISO6709=+47.2783-002.4948/
`
	v := parseProbe(out)

	assert.True(t, v.HasSite)
	assert.InDelta(t, 47.2783, v.LatDeg, 1e-6)
	assert.InDelta(t, -2.4948, v.LonDeg, 1e-6)
	assert.Equal(t, time.Date(2026, 8, 12, 18, 11, 49, 0, time.UTC).UnixMilli(), v.CreatedMs)
	assert.Equal(t, 31484, v.Frames, "30 fps over 1049.45 s")
}

func TestParseProbe_NoLocationTagLeavesHasSiteFalse(t *testing.T) {
	v := parseProbe("width=1920\nheight=1080\nr_frame_rate=30/1\nduration=10\n")

	assert.False(t, v.HasSite, "a clip without the tag must not read as a site at 0N 0E")
	assert.Zero(t, v.LatDeg)
}
