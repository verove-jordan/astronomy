package lightpollution

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeAtlas writes a synthetic grid (+ sidecar) and returns the .bin path. Cells are row-major,
// row 0 = north (LatMax). Shared by the atlas and provider tests.
func writeAtlas(t *testing.T, meta atlasMeta, cells []float32) string {
	t.Helper()
	require.Len(t, cells, meta.Rows*meta.Cols)
	dir := t.TempDir()
	bin := filepath.Join(dir, "atlas.bin")
	buf := new(bytes.Buffer)
	for _, v := range cells {
		require.NoError(t, binary.Write(buf, binary.LittleEndian, v))
	}
	require.NoError(t, os.WriteFile(bin, buf.Bytes(), 0o644))
	mb, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atlas.json"), mb, 0o644))
	return bin
}

// unitGrid is a 2×2 SQM atlas over lat[0,1] lon[0,1]: NW=21, NE=20, SW=19, SE=18.
func unitGrid(t *testing.T) *atlas {
	bin := writeAtlas(t, atlasMeta{
		Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "sqm", NoData: -1,
	}, []float32{21, 20, 19, 18})
	a := loadAtlas(bin)
	require.NotNil(t, a)
	return a
}

func TestAtlas_Sample_Corners(t *testing.T) {
	a := unitGrid(t)
	tests := []struct {
		name     string
		lat, lon float64
		want     float64
	}{
		{"NW", 1, 0, 21},
		{"NE", 1, 1, 20},
		{"SW", 0, 0, 19},
		{"SE", 0, 1, 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := a.sampleSQM(tt.lat, tt.lon)
			require.True(t, ok)
			assert.InDelta(t, tt.want, got, 0.001)
		})
	}
}

func TestAtlas_Sample_BilinearCenter(t *testing.T) {
	a := unitGrid(t)
	got, ok := a.sampleSQM(0.5, 0.5)
	require.True(t, ok)
	assert.InDelta(t, 19.5, got, 0.001) // mean of 21,20,19,18
}

func TestAtlas_Sample_OutOfBounds(t *testing.T) {
	a := unitGrid(t)
	_, ok := a.sampleSQM(2, 2)
	assert.False(t, ok)
	_, ok = a.sampleSQM(-0.5, 0.5)
	assert.False(t, ok)
}

func TestAtlas_Sample_SkipsNoData(t *testing.T) {
	// SW is nodata; sampling exactly there falls back to ok=false (its only neighbour weight is itself).
	bin := writeAtlas(t, atlasMeta{
		Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "sqm", NoData: -1,
	}, []float32{21, 20, -1, 18})
	a := loadAtlas(bin)
	require.NotNil(t, a)
	_, ok := a.sampleSQM(0, 0)
	assert.False(t, ok)
	// The center still interpolates from the three valid corners.
	got, ok := a.sampleSQM(0.5, 0.5)
	require.True(t, ok)
	assert.InDelta(t, (21+20+18)/3.0, got, 0.001)
}

func TestLoadAtlas_MissingOrBad(t *testing.T) {
	assert.Nil(t, loadAtlas(filepath.Join(t.TempDir(), "nope.bin")))

	// Sidecar present but the binary is too small for the declared grid.
	dir := t.TempDir()
	bin := filepath.Join(dir, "atlas.bin")
	require.NoError(t, os.WriteFile(bin, []byte{0, 0, 0, 0}, 0o644))
	mb, _ := json.Marshal(atlasMeta{Rows: 4, Cols: 4, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "atlas.json"), mb, 0o644))
	assert.Nil(t, loadAtlas(bin))
}
