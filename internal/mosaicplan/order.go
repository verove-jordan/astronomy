package mosaicplan

import "fmt"

// orderTiles assigns the serpentine capture order — rows top to bottom, alternating direction so
// consecutive tiles are always adjacent (minimal hand-controller slews) — and derives each tile's
// capture folder name from it. "p%02d" is THE panel-folder convention: the capture assistant
// instructs it, and the processing pipeline's panel segmentation matches it.
func orderTiles(tiles []Tile, grid Grid) {
	order := 1
	for row := 0; row < grid.Rows; row++ {
		for i := 0; i < grid.Cols; i++ {
			col := i
			if row%2 == 1 {
				col = grid.Cols - 1 - i
			}
			t := &tiles[row*grid.Cols+col]
			t.Order = order
			t.Folder = fmt.Sprintf("p%02d", order)
			order++
		}
	}
}
