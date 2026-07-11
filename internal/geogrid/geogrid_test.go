package geogrid

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeGrid writes a synthetic row-major float32 grid + JSON sidecar in a temp dir and returns the bin path.
func writeGrid(t *testing.T, m Meta, cells []float32) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "g.bin")
	buf := make([]byte, len(cells)*4)
	for i, v := range cells {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	require.NoError(t, os.WriteFile(bin, buf, 0o644))
	mb, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "g.json"), mb, 0o644))
	return bin
}

func TestGridSample(t *testing.T) {
	// 2×2 over [0,1]×[0,1], north-up row-major: [NW, NE, SW, SE] = [10, 20, 30, 40].
	m := Meta{Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "meters", NoData: -1}
	g := Load(writeGrid(t, m, []float32{10, 20, 30, 40}))
	require.NotNil(t, g)

	t.Run("bilinear centre = mean of neighbours", func(t *testing.T) {
		v, ok := g.SampleBilinear(0.5, 0.5)
		require.True(t, ok)
		assert.InDelta(t, 25.0, v, 1e-6)
	})
	t.Run("max centre = tallest neighbour", func(t *testing.T) {
		v, ok := g.SampleMax(0.5, 0.5)
		require.True(t, ok)
		assert.Equal(t, 40.0, v)
	})
	t.Run("NW corner", func(t *testing.T) {
		v, ok := g.SampleBilinear(1, 0) // LatMax, LonMin
		require.True(t, ok)
		assert.InDelta(t, 10.0, v, 1e-6)
	})
	t.Run("out of bounds", func(t *testing.T) {
		_, ok := g.SampleBilinear(2, 2)
		assert.False(t, ok)
		_, ok = g.SampleMax(-1, 0)
		assert.False(t, ok)
	})
}

func TestSampleMaxSkipsNodata(t *testing.T) {
	m := Meta{Rows: 2, Cols: 2, LatMin: 0, LatMax: 1, LonMin: 0, LonMax: 1, Unit: "meters", NoData: -1}
	g := Load(writeGrid(t, m, []float32{-1, -1, -1, 15})) // only SE valid
	require.NotNil(t, g)
	v, ok := g.SampleMax(0.5, 0.5)
	require.True(t, ok)
	assert.Equal(t, 15.0, v)
}

func TestLoadMissingIsNil(t *testing.T) {
	assert.Nil(t, Load(filepath.Join(t.TempDir(), "nope.bin")))
}
