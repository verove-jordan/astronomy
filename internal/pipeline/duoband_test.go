package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// writeColourMaster puts a 3-plane FITS at dir/name.fits with the given per-plane constant values.
func writeColourMaster(t *testing.T, dir, name string, r, g, b float32) string {
	t.Helper()
	im := fits.NewImage(4, 3, 3)
	for i := range im.Pix[0] {
		im.Pix[0][i], im.Pix[1][i], im.Pix[2][i] = r, g, b
	}
	p := filepath.Join(dir, name+".fits")
	require.NoError(t, im.WriteFITS(p))
	return p
}

func TestSplitDuoband(t *testing.T) {
	tests := []struct {
		name        string
		r, g, b     float32
		wantHa      float32
		wantOIII    float32
		explanation string
	}{
		{
			name: "green carries the stronger [OIII]", r: 0.40, g: 0.20, b: 0.10,
			wantHa: 0.40, wantOIII: 0.20,
			explanation: "the 500.7 nm line sits between the passbands; keep whichever pixel caught more",
		},
		{
			name: "blue carries the stronger [OIII]", r: 0.40, g: 0.10, b: 0.25,
			wantHa: 0.40, wantOIII: 0.25,
			explanation: "averaging would have halved it to 0.175 and cost SNR",
		},
		{
			name: "equal green and blue", r: 0.9, g: 0.3, b: 0.3,
			wantHa: 0.9, wantOIII: 0.3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			master := writeColourMaster(t, dir, "master_RGB", tt.r, tt.g, tt.b)

			haBase, oiiiBase, err := splitDuoband(master, dir)
			require.NoError(t, err)
			assert.Equal(t, duobandHaBase, haBase)
			assert.Equal(t, duobandOIIIBase, oiiiBase)

			ha, err := fits.ReadImage(filepath.Join(dir, haBase+".fits"))
			require.NoError(t, err)
			oiii, err := fits.ReadImage(filepath.Join(dir, oiiiBase+".fits"))
			require.NoError(t, err)

			require.Equal(t, 1, ha.C, "the emission channels are single-plane")
			require.Equal(t, 1, oiii.C)
			assert.InDelta(t, tt.wantHa, ha.Pix[0][0], 1e-6, "Hα is the red plane")
			assert.InDelta(t, tt.wantOIII, oiii.Pix[0][0], 1e-6, tt.explanation)
		})
	}
}

func TestSplitDuoband_NeedsThreePlanes(t *testing.T) {
	dir := t.TempDir()
	im := fits.NewImage(4, 3, 1)
	p := filepath.Join(dir, "mono.fits")
	require.NoError(t, im.WriteFITS(p))

	_, _, err := splitDuoband(p, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "need 3")
}

func TestDuobandChannels(t *testing.T) {
	colour := func(palette string) *mode.Preset { return &mode.Preset{Color: mode.OSC, Palette: palette} }

	tests := []struct {
		name      string
		preset    *mode.Preset
		wantSplit bool
		why       string
	}{
		{"a duo-band colour run asking for foraxx is split", colour("foraxx"), true,
			"the Hubble-like rendition needs real Hα and [OIII]"},
		{"hoo is split too", colour("hoo"), true, ""},
		{"a natural colour run is left alone", colour("natural"), false,
			"an ordinary OSC run keeps the pass-through it has always had"},
		{"an empty palette is left alone", colour(""), false, ""},
		{"a mono run is left alone", &mode.Preset{Color: mode.Mono, Palette: "foraxx"}, false,
			"a filter wheel already supplies the real channels"},
		{"a nil preset is left alone", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeColourMaster(t, dir, "master_RGB", 0.4, 0.2, 0.1)
			in := map[string]string{"RGB": "master_RGB"}

			out, note := duobandChannels(tt.preset, in, dir)

			assert.Len(t, in, 1, "the caller's map is never mutated")
			if !tt.wantSplit {
				assert.Equal(t, in, out)
				assert.Empty(t, note)
				return
			}
			assert.Equal(t, duobandHaBase, out["Ha"], tt.why)
			assert.Equal(t, duobandOIIIBase, out["OIII"])
			assert.Equal(t, "master_RGB", out["RGB"], "the colour master stays available")
			assert.Contains(t, note, "duo-band")
		})
	}
}

func TestResolvePalette_Duoband(t *testing.T) {
	split := map[string]string{"RGB": "master_RGB", "Ha": duobandHaBase, "OIII": duobandOIIIBase}
	plain := map[string]string{"RGB": "master_RGB"}

	tests := []struct {
		name     string
		preset   *mode.Preset
		channels map[string]string
		wantName string
		wantNB   bool
	}{
		{
			name:   "a split duo-band run reaches foraxx",
			preset: &mode.Preset{Color: mode.OSC, Palette: "foraxx"}, channels: split,
			wantName: "foraxx", wantNB: true,
		},
		{
			name:   "a split duo-band run reaches hoo",
			preset: &mode.Preset{Color: mode.OSC, Palette: "hoo"}, channels: split,
			wantName: "hoo", wantNB: true,
		},
		{
			// A duo-band filter passes no [SII], so sho falls back down its own chain.
			name:   "sho falls back to hoo, since duo-band has no [SII]",
			preset: &mode.Preset{Color: mode.OSC, Palette: "sho"}, channels: split,
			wantName: "hoo", wantNB: true,
		},
		{
			// The regression that matters: an ordinary colour run must not change at all.
			name:   "an unsplit colour run still passes through",
			preset: &mode.Preset{Color: mode.OSC, Palette: "foraxx"}, channels: plain,
			wantName: "rgb", wantNB: false,
		},
		{
			name:   "a natural colour run still passes through",
			preset: &mode.Preset{Color: mode.OSC, Palette: "natural"}, channels: split,
			wantName: "rgb", wantNB: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pal, _ := resolvePalette(tt.preset, tt.channels)
			assert.Equal(t, tt.wantName, pal.Name)
			assert.Equal(t, tt.wantNB, pal.Narrowband)
			assert.True(t, pal.Color, "every one of these renders in colour")
		})
	}
}
