package canopy

import (
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
