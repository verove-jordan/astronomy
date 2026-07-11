package canopy

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEthTileName(t *testing.T) {
	assert.Equal(t, "N45E003", ethTileName(45, 3))
	assert.Equal(t, "N48E000", ethTileName(48, 0))
	assert.Equal(t, "N48W003", ethTileName(48, -3))
	assert.Equal(t, "S03W072", ethTileName(-3, -72))
}

func TestSourceTiles(t *testing.T) {
	// A small box inside one 3° tile → exactly that tile.
	assert.Equal(t, []string{"N45E003"},
		sourceTiles(Bounds{MinLat: 47, MinLon: 4, MaxLat: 48, MaxLon: 5}, ""))

	// URL-template substitution.
	urls := sourceTiles(Bounds{MinLat: 47, MinLon: 4, MaxLat: 48, MaxLon: 5}, "http://x/{tile}.tif")
	require.Len(t, urls, 1)
	assert.Equal(t, "http://x/N45E003.tif", urls[0])

	// A box spanning 3×3 tiles.
	assert.Equal(t, 9, TileCount(Bounds{MinLat: 44, MinLon: 2, MaxLat: 49, MaxLon: 7}))
}

func TestBuildWarpArgs(t *testing.T) {
	b := Bounds{MinLat: 45, MinLon: 3, MaxLat: 45.2, MaxLon: 3.3}
	args := buildWarpArgs(b, 0.0008, "/tmp/mosaic.vrt", "/tmp/atlas.bin")

	// -te must be xmin ymin xmax ymax = minLon minLat maxLon maxLat. A lat/lon swap here silently warps the
	// wrong window (and is exactly the kind of regression this guards).
	te := slices.Index(args, "-te")
	require.GreaterOrEqual(t, te, 0, "-te present")
	require.Less(t, te+4, len(args))
	assert.Equal(t, []string{"3", "45", "3.3", "45.2"}, args[te+1:te+5])

	// -tr carries the requested resolution.
	tr := slices.Index(args, "-tr")
	require.GreaterOrEqual(t, tr, 0)
	assert.Equal(t, []string{"0.0008", "0.0008"}, args[tr+1:tr+3])

	// GDAL_DISABLE_READDIR_ON_OPEN=EMPTY_DIR must NOT be here: it makes gdalwarp fail to open the VRT's
	// source COG and die with no error — the download bug. Guard against it being re-added.
	assert.NotContains(t, args, "GDAL_DISABLE_READDIR_ON_OPEN")

	// Source then destination are the final positional args.
	assert.Equal(t, []string{"/tmp/mosaic.vrt", "/tmp/atlas.bin"}, args[len(args)-2:])
}

func TestResolveBounds(t *testing.T) {
	b, err := ResolveBounds("france", "")
	require.NoError(t, err)
	assert.Equal(t, RegionBounds["france"], b)

	b, err = ResolveBounds("", "47,4,48,5")
	require.NoError(t, err)
	assert.Equal(t, Bounds{MinLat: 47, MinLon: 4, MaxLat: 48, MaxLon: 5}, b)

	_, err = ResolveBounds("atlantis", "")
	assert.Error(t, err)
}
