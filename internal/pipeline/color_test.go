package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
)

func TestNeedsDebayer(t *testing.T) {
	cfa := func(p string) *inspect.Frame { return &inspect.Frame{Path: p, Bayer: "RGGB", Channels: 1} }
	rgb := func(p string) *inspect.Frame { return &inspect.Frame{Path: p, Channels: 3} }
	mono := func(p string) *inspect.Frame { return &inspect.Frame{Path: p, Channels: 1} }

	tests := []struct {
		name   string
		frames []*inspect.Frame
		want   bool
	}{
		{"raw CFA mosaic", []*inspect.Frame{cfa("a.fits"), cfa("b.fits")}, true},
		{"already demosaiced RGB", []*inspect.Frame{rgb("a.tif"), rgb("b.tif")}, false},
		{"monochrome", []*inspect.Frame{mono("a.fits")}, false},
		// Debayering an already-RGB frame corrupts it; NOT debayering a CFA frame only makes a visible
		// checkerboard. When a group disagrees, take the recoverable failure.
		{"mixed group is treated as already-RGB", []*inspect.Frame{cfa("a.fits"), rgb("b.fits")}, false},
		{"empty group", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, needsDebayer(tt.frames))
		})
	}
}

func TestSeqIngest(t *testing.T) {
	fr := func(paths ...string) []*inspect.Frame {
		out := make([]*inspect.Frame, len(paths))
		for i, p := range paths {
			out[i] = &inspect.Frame{Path: p}
		}
		return out
	}
	tests := []struct {
		name   string
		frames []*inspect.Frame
		want   siril.SeqIngest
	}{
		// FITS keeps `link`, so every existing monochrome run emits a byte-identical script.
		{"fits links", fr("/x/a.fits", "/x/b.fit"), siril.IngestLink},
		{"nikon raw converts", fr("/x/DSC_0001.NEF"), siril.IngestConvert},
		{"colour tiff converts", fr("/x/a.tif"), siril.IngestConvert},
		{"one non-fits frame is enough", fr("/x/a.fits", "/x/b.dng"), siril.IngestConvert},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, seqIngest(tt.frames))
		})
	}
}

// TestResolvePalette_OneShotColor pins the palette bypass. Every palette spec is written in terms of
// filter names, so without this a colour run walked the fallback chain to "mono" and threw its colour
// away — producing a greyscale result from a colour camera.
func TestResolvePalette_OneShotColor(t *testing.T) {
	channels := map[string]string{"RGB": "master_RGB"}

	pal, note := resolvePalette(&mode.Preset{Color: mode.OSC, Palette: "sho"}, channels)
	assert.True(t, pal.Color, "a one-shot-color run must finish in colour")
	assert.False(t, pal.Mono)
	assert.False(t, pal.Narrowband)
	assert.False(t, pal.HaScreen, "there are no emission channels to screen")
	assert.Empty(t, note, "the requested palette is not 'unavailable' — it simply does not apply")

	// A mono preset with the same single channel still falls back to mono, as before.
	monoPal, _ := resolvePalette(&mode.Preset{Color: mode.Mono, Palette: "sho"}, map[string]string{"L": "master_L"})
	assert.True(t, monoPal.Mono)
}

func TestOSCSource(t *testing.T) {
	assert.Equal(t, "master_RGB", oscSource(map[string]string{"RGB": "master_RGB"}))
	// A single channel under an unexpected tag still resolves — there is only one thing it can be.
	assert.Equal(t, "master_X", oscSource(map[string]string{"weird": "master_X"}))
	assert.Empty(t, oscSource(nil))
}

func TestMarkColorPreset(t *testing.T) {
	var p mode.Preset
	assert.False(t, isColorRun(&p))
	markColorPreset(&p)
	assert.True(t, isColorRun(&p))
	assert.Equal(t, mode.OSC, p.Color)
	markColorPreset(nil) // must not panic: several modes carry an optional preset
	assert.False(t, isColorRun(nil))
}
