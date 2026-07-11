package lightpollution

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeColoredTile drops a placeholder recolored tile at <cacheDir>/tiles_bortle_v{ver}/z/x/y.png.
func writeFakeColoredTile(t *testing.T, cacheDir string, ver, z, x, y int) string {
	t.Helper()
	p := filepath.Join(cacheDir, "tiles_bortle_v"+strconv.Itoa(ver),
		strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("png"), 0o644))
	return p
}

// TestInvalidateColoredTiles_ScopedToBounds is the regression guard for the cache-wipe storm: a rebuild
// must drop ONLY the recolored tiles overlapping the rebuilt region, leaving far-away tiles cached.
func TestInvalidateColoredTiles_ScopedToBounds(t *testing.T) {
	cacheDir := t.TempDir()
	// z6 tile (32,21) covers ~western Europe (lon[0,5.6], lat[49,52.6]); (10,40) is over the South Pacific.
	inside := writeFakeColoredTile(t, cacheDir, coloredCacheVersion, 6, 32, 21)
	outside := writeFakeColoredTile(t, cacheDir, coloredCacheVersion, 6, 10, 40)
	// A stale tile in an OLDER cache version dir must also be scoped (glob covers tiles_bortle_v*).
	insideOld := writeFakeColoredTile(t, cacheDir, coloredCacheVersion-1, 6, 32, 21)

	b := Bounds{MinLat: 48, MinLon: -1, MaxLat: 53, MaxLon: 6} // a box over western Europe
	invalidateColoredTiles(cacheDir, b)

	assert.NoFileExists(t, inside, "a tile overlapping the rebuilt region is dropped")
	assert.NoFileExists(t, insideOld, "overlapping tiles in older cache-version dirs are dropped too")
	assert.FileExists(t, outside, "a tile outside the rebuilt region is preserved (no full-world storm)")
}

func TestTileIntersectsBounds(t *testing.T) {
	// The western-Europe tile intersects a European box but not an Australian one.
	eu := Bounds{MinLat: 45, MinLon: -2, MaxLat: 55, MaxLon: 8}
	au := Bounds{MinLat: -40, MinLon: 130, MaxLat: -20, MaxLon: 155}
	assert.True(t, tileIntersectsBounds(6, 32, 21, eu))
	assert.False(t, tileIntersectsBounds(6, 32, 21, au))
}
