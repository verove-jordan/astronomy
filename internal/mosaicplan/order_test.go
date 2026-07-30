package mosaicplan

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrderTiles_Serpentine3x3(t *testing.T) {
	grid := Grid{Rows: 3, Cols: 3}
	tiles := make([]Tile, 9)
	for i := range tiles {
		tiles[i] = Tile{Index: i, Row: i / 3, Col: i % 3}
	}
	orderTiles(tiles, grid)

	wantOrder := map[[2]int]int{ // row 0 left→right, row 1 right→left, row 2 left→right
		{0, 0}: 1, {0, 1}: 2, {0, 2}: 3,
		{1, 2}: 4, {1, 1}: 5, {1, 0}: 6,
		{2, 0}: 7, {2, 1}: 8, {2, 2}: 9,
	}
	for _, tile := range tiles {
		require.Equal(t, wantOrder[[2]int{tile.Row, tile.Col}], tile.Order, "tile r%dc%d", tile.Row, tile.Col)
		assert.Equal(t, fmt.Sprintf("p%02d", tile.Order), tile.Folder)
	}
}

func TestOrderTiles_ConsecutiveTilesAreAdjacent(t *testing.T) {
	grid := Grid{Rows: 4, Cols: 3}
	tiles := make([]Tile, 12)
	for i := range tiles {
		tiles[i] = Tile{Index: i, Row: i / 3, Col: i % 3}
	}
	orderTiles(tiles, grid)

	byOrder := make([]Tile, len(tiles)+1)
	for _, tile := range tiles {
		byOrder[tile.Order] = tile
	}
	for o := 1; o < len(tiles); o++ {
		a, b := byOrder[o], byOrder[o+1]
		dist := absInt(a.Row-b.Row) + absInt(a.Col-b.Col)
		assert.Equal(t, 1, dist, "order %d→%d must be one slew", o, o+1)
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
