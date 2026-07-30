package mosaic

// Plan is the read model of a saved mosaic plan (resolved by the job manager from Postgres; the
// pipeline stays DB-free). Tiles are in capture order.
type Plan struct {
	ID                  int64
	Name, Target        string
	CenterRA, CenterDec float64 // deg J2000
	CameraPADeg         float64
	OverlapFrac         float64
	Cols, Rows          int
	Tiles               []Tile
}

// Tile is one planned pointing of the mosaic grid. Folder is the capture-subfolder convention
// ("p01"…) pinned by the tile planner (internal/mosaicplan); SegmentPanels reuses it as the stable
// panel label when a detected panel matches this tile.
type Tile struct {
	Row, Col, Order int
	Folder          string // "p01"…
	RA, Dec         float64
}
