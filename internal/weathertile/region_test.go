package weathertile

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTileRegion_NeighboursShareOneCube(t *testing.T) {
	// Two adjacent z8 tiles inside the same 8×8 block must resolve to the identical region, so a viewport's
	// tiles reuse one cached cube (and render continuously across the tile edge).
	a := regionOf(TileRegion(8, 130, 90))
	b := regionOf(TileRegion(8, 131, 90)) // +1 in x, same block (130,131 share 128..135)
	assert.Equal(t, a, b, "adjacent tiles in a block share a region")

	// A tile in the next block gets a different (adjacent) region.
	c := regionOf(TileRegion(8, 136, 90)) // next block starts at 136
	assert.NotEqual(t, a, c)
}

func TestTileRegion_CoversTheTile(t *testing.T) {
	// The region radius must span its block, so a tile inside it is covered (no permanent seam).
	for _, z := range []int{4, 6, 8, 10} {
		n := 1 << z // tile indices run 0..n-1 at this zoom
		cLat, cLon, radius := TileRegion(z, n/2, n/3)
		assert.Greater(t, radius, 0.0, "z=%d radius positive", z)
		// The region centre is a sane lat/lon.
		assert.GreaterOrEqual(t, cLat, -90.0)
		assert.LessOrEqual(t, cLat, 90.0)
		assert.GreaterOrEqual(t, cLon, -180.0)
		assert.LessOrEqual(t, cLon, 180.0)
	}
}

func TestTileRegion_DeepZoomCollapsesToCapScale(t *testing.T) {
	// Past the zoom cap a tile resolves its z8 ancestor's region: zooming from z8 to z12 must NOT mint a
	// new region scale (each scale is a fresh multi-hundred-point upstream fetch).
	assert.Equal(t,
		regionOf(TileRegion(8, 130, 90)),
		regionOf(TileRegion(12, 130<<4, 90<<4)),
		"a z12 descendant shares its z8 ancestor's region")
	assert.Equal(t,
		regionOf(TileRegion(8, 130, 90)),
		regionOf(TileRegion(12, 130<<4+15, 90<<4+15)),
		"the far corner of the ancestor tile still folds to the same region")
}

func TestLatLonToTile_RoundTrip(t *testing.T) {
	// Tile centres computed from tile2lon/tile2lat must map back to the same (x,y).
	for _, tc := range []struct{ z, x, y int }{{7, 64, 44}, {8, 130, 90}, {3, 4, 2}, {0, 0, 0}} {
		lonC := (tile2lon(tc.x, tc.z) + tile2lon(tc.x+1, tc.z)) / 2
		latC := (tile2lat(tc.y, tc.z) + tile2lat(tc.y+1, tc.z)) / 2
		x, y := LatLonToTile(latC, lonC, tc.z)
		assert.Equal(t, tc.x, x, "z=%d", tc.z)
		assert.Equal(t, tc.y, y, "z=%d", tc.z)
	}
	// Pole/out-of-range latitudes clamp instead of overflowing.
	x, y := LatLonToTile(90, 0, 8)
	assert.GreaterOrEqual(t, x, 0)
	assert.Equal(t, 0, y, "north pole clamps to the top row")
}

func TestFrameIndex(t *testing.T) {
	ts := []int64{1000, 2000, 3000}
	assert.Equal(t, 1, FrameIndex(ts, 2000))   // exact
	assert.Equal(t, 0, FrameIndex(ts, 1200))   // nearest (1000)
	assert.Equal(t, 2, FrameIndex(ts, 2600))   // nearest (3000)
	assert.Equal(t, 2, FrameIndex(ts, 9999))   // clamps to the last
	assert.Equal(t, -1, FrameIndex(nil, 1000)) // empty
}

// regionOf rounds a region tuple so equality comparisons ignore float noise.
func regionOf(lat, lon, r float64) [3]int64 {
	return [3]int64{int64(lat * 1e6), int64(lon * 1e6), int64(r * 1e6)}
}
