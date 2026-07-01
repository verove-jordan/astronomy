package lightpollution

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gzipTile gzips a raw djlorenz byte payload for the decoder tests.
func gzipTile(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(raw)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// uniformTileRaw builds a 600×600 tile whose every sample decodes to `base` (all deltas zero).
func uniformTileRaw(base uint8) []byte {
	raw := make([]byte, djTilePx*djTilePx+1)
	raw[0] = 0
	raw[1] = base
	return raw
}

func TestCompressedToSQM(t *testing.T) {
	assert.InDelta(t, 22.0, compressedToSQM(0), 1e-4, "pristine: compressed 0 → 22.0")
	assert.InDelta(t, 21.25, compressedToSQM(189), 0.02, "ratio≈1 near compressed 189 → ~21.25")
	// Monotone: a brighter (higher compressed) sky must read a LOWER SQM.
	prev := compressedToSQM(0)
	for x := 10; x <= 400; x += 10 {
		cur := compressedToSQM(x)
		assert.Less(t, cur, prev, "SQM must decrease as compressed rises")
		prev = cur
	}
}

func TestDjTileIndex(t *testing.T) {
	tests := []struct {
		name         string
		lat, lon     float64
		wantX, wantY int
	}{
		{"Montigny-sur-Loing", 48.104, 2.957, 37, 23},
		{"Paris", 48.857, 2.352, 37, 23},
		{"Brest", 48.39, -4.49, 36, 23},
		{"Nice", 43.70, 7.27, 38, 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantX, djTileX(tt.lon), "tilex")
			assert.Equal(t, tt.wantY, djTileY(tt.lat), "tiley")
		})
	}
}

func TestDecodeDjlorenzTile_Uniform(t *testing.T) {
	grid, err := decodeDjlorenzTile(gzipTile(t, uniformTileRaw(0)))
	require.NoError(t, err)
	require.Len(t, grid, djTilePx*djTilePx)
	for _, v := range grid {
		assert.InDelta(t, 22.0, v, 1e-4)
	}
}

func TestDecodeDjlorenzTile_DeltaPath(t *testing.T) {
	// base 0 at the SW corner, one latitude delta (+10 into row 2, col 1) and one longitude delta
	// (+5 into col 2, row 1). Everything else stays 0 → SQM 22.0.
	raw := make([]byte, djTilePx*djTilePx+1)
	raw[djTilePx*1+1] = 10 // latitude delta into iy=2 (col 1)
	raw[2] = 5             // longitude delta into ix=2 (row 1)
	grid, err := decodeDjlorenzTile(gzipTile(t, raw))
	require.NoError(t, err)

	at := func(ix, iy int) float32 { return grid[(iy-1)*djTilePx+(ix-1)] }
	assert.InDelta(t, 22.0, at(1, 1), 1e-4, "SW corner = base 0")
	assert.InDelta(t, compressedToSQM(5), at(2, 1), 1e-4, "one longitude step east")
	assert.InDelta(t, compressedToSQM(10), at(1, 2), 1e-4, "one latitude step north")
	// Deltas are cumulative: the +5 persists eastward along row 1 (ix=3 delta is 0 → still compressed 5),
	// while row 2 starts fresh from its own col-1 value (the +10 latitude offset), unaffected by row 1's +5.
	assert.InDelta(t, compressedToSQM(5), at(3, 1), 1e-4, "row 1 keeps the cumulative +5 eastward")
	assert.InDelta(t, compressedToSQM(10), at(2, 2), 1e-4, "row 2 = its col-1 latitude offset, not row 1's +5")
}

func TestDecodeDjlorenzTile_TooShort(t *testing.T) {
	_, err := decodeDjlorenzTile(gzipTile(t, []byte{0, 0, 0}))
	require.Error(t, err)
}

func TestBuildAtlasGrid_StitchesTiles(t *testing.T) {
	// A bbox spanning two horizontally-adjacent tiles (37 & 38): west tile uniform Bortle-2-ish (SQM ~21.7,
	// compressed small), east tile brighter. Verify mosaic width, north-up placement, and meta bounds.
	west := gzipTile(t, uniformTileRaw(10))
	east := gzipTile(t, uniformTileRaw(60))
	fetch := func(tx, ty int) ([]byte, error) {
		if tx == 38 {
			return east, nil
		}
		return west, nil
	}
	// lon 3..7 → tiles 37,38; lat 43..44 → tile 22.
	b := Bounds{MinLat: 43, MinLon: 3, MaxLat: 44, MaxLon: 7}
	cells, meta, err := buildAtlasGrid(b, fetch)
	require.NoError(t, err)

	assert.Equal(t, 2*djTilePx, meta.Cols, "two tiles wide")
	assert.Equal(t, 1*djTilePx, meta.Rows, "one tile tall")
	require.Len(t, cells, meta.Rows*meta.Cols)

	// West half is the darker value, east half the brighter one.
	assert.InDelta(t, compressedToSQM(10), cells[0], 1e-4, "NW cell from west tile")
	assert.InDelta(t, compressedToSQM(60), cells[djTilePx], 1e-4, "first cell of the east tile")
	assert.Less(t, cells[djTilePx], cells[0], "east tile is brighter (lower SQM)")

	// Meta bounds are the pixel-centre extremes of tiles x∈{37,38}, y=22.
	assert.InDelta(t, 5.0*36-180+djHalfCell, meta.LonMin, 1e-6) // tile 37 west edge
	assert.InDelta(t, 5.0*38-180-djHalfCell, meta.LonMax, 1e-6) // tile 38 east edge
	assert.InDelta(t, 5.0*22-65-djHalfCell, meta.LatMax, 1e-6)
	assert.InDelta(t, 5.0*21-65+djHalfCell, meta.LatMin, 1e-6)
	assert.Equal(t, "sqm", meta.Unit)
}

func TestBuildAtlasGrid_SkipsFailedTiles(t *testing.T) {
	// A tile that fails to fetch leaves its cells as nodata (-1); the reader then falls back for them.
	fetch := func(tx, ty int) ([]byte, error) { return nil, assert.AnError }
	cells, _, err := buildAtlasGrid(Bounds{MinLat: 43, MinLon: 3, MaxLat: 44, MaxLon: 4}, fetch)
	require.NoError(t, err)
	for _, v := range cells {
		assert.Equal(t, float32(-1), v)
	}
}
