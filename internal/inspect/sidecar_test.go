package inspect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSharpcapSidecar(t *testing.T) {
	tests := []struct {
		name string
		body string
		want sidecarMeta
	}{
		{
			name: "ZWO ASI capture settings",
			body: "[ZWO ASI1600MM Pro]\nEFW Slot = 1(Alias: 1)\nExposure = 30s\nGain = 300\nTemperature = -15.0 C\n",
			want: sidecarMeta{Slot: 1, Gain: 300, HasGain: true, ExposureMs: 30000, TempMilliC: -15000, HasTemp: true},
		},
		{
			name: "millisecond exposure, slot 5, filter alias",
			body: "EFW Slot = 5(Alias: Ha)\nExposure = 1500ms\n",
			want: sidecarMeta{Slot: 5, SlotAlias: "Ha", ExposureMs: 1500},
		},
		{
			name: "alias canonicalized through filterToken",
			body: "EFW Slot = 3(Alias: green)\n",
			want: sidecarMeta{Slot: 3, SlotAlias: "G"},
		},
		{
			name: "non-filter alias ignored",
			body: "EFW Slot = 2(Alias: Moon)\nGain = 0\n",
			want: sidecarMeta{Slot: 2, HasGain: true},
		},
		{
			name: "no EFW line",
			body: "Gain = 200\nExposure = 60s\n",
			want: sidecarMeta{Gain: 200, HasGain: true, ExposureMs: 60000},
		},
		{
			// ZWO writes the offset as "Brightness"; the unrelated "Auto Exp Target Brightness" must NOT be
			// read as the offset (full-key match, not substring). This is a real ASI1600 bias sidecar.
			name: "ZWO Brightness is the offset (offset 10, not Auto Exp Target Brightness 100)",
			body: "[ZWO ASI1600MM Pro]\nAuto Exp Target Brightness = 100\nBrightness = 10\nEFW Slot = 4(Alias: B)\nExposure = 10ms\nGain = 0\nTemperature = -17.0 C\n",
			want: sidecarMeta{Slot: 4, SlotAlias: "B", Gain: 0, HasGain: true, Offset: 10, HasOffset: true, ExposureMs: 10, TempMilliC: -17000, HasTemp: true},
		},
		{
			name: "generic Offset key (non-SharpCap software)",
			body: "Gain = 100\nOffset = 21\n",
			want: sidecarMeta{Gain: 100, HasGain: true, Offset: 21, HasOffset: true},
		},
		{
			name: "blank/garbage",
			body: "\n[header]\nrandom text\n",
			want: sidecarMeta{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseSharpcapSidecar(tt.body))
		})
	}
}

func TestReadSharpcapSidecar(t *testing.T) {
	dir := t.TempDir()
	fitsPath := filepath.Join(dir, "x.FIT")
	require.NoError(t, os.WriteFile(fitsPath, []byte("placeholder"), 0o644))
	require.NoError(t, os.WriteFile(fitsPath+".txt", []byte("EFW Slot = 2(Alias: 2)\nGain = 250\n"), 0o644))

	m, ok := readSharpcapSidecar(fitsPath)
	require.True(t, ok)
	assert.Equal(t, 2, m.Slot)
	assert.Equal(t, int64(250), m.Gain)

	_, ok = readSharpcapSidecar(filepath.Join(dir, "missing.FIT"))
	assert.False(t, ok, "absent sidecar → ok=false")
}
