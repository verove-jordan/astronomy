package noise

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteHeatmapPNG_Dimensions(t *testing.T) {
	rng := newRNG(31)
	const w, h = 256, 192
	p := newPlane(w, h, 0.05)
	addNoise(p, rng, 2e-3)
	rep := Measure(monoImage(w, h, p))
	require.NotEmpty(t, rep.Tiles)

	path := filepath.Join(t.TempDir(), "heatmap.png")
	require.NoError(t, WriteHeatmapPNG(rep, path))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	require.NoError(t, err)
	assert.Equal(t, rep.GridW*heatmapScale, cfg.Width)
	assert.Equal(t, rep.GridH*heatmapScale, cfg.Height)
}

func TestWriteHeatmapPNG_BrightTileIsBrighter(t *testing.T) {
	// A grid with one hot tile must map that tile to a brighter block than a quiet tile.
	rep := Report{GridW: 3, GridH: 1, Tile: tileSize, Tiles: []float32{1e-3, 5e-3, 1e-3}}
	path := filepath.Join(t.TempDir(), "hm.png")
	require.NoError(t, WriteHeatmapPNG(rep, path))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	img, err := png.Decode(f)
	require.NoError(t, err)
	g := img.(*image.Gray)
	quiet := g.GrayAt(1, 1).Y            // inside the first tile block
	hot := g.GrayAt(heatmapScale+1, 1).Y // inside the middle (hot) tile block
	assert.Greater(t, hot, quiet)
}

func TestWriteHeatmapPNG_BadGrid(t *testing.T) {
	err := WriteHeatmapPNG(Report{GridW: 0, GridH: 0}, filepath.Join(t.TempDir(), "x.png"))
	assert.Error(t, err)
}
