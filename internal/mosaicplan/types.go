package mosaicplan

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// MaxGridPerAxis caps the auto-computed grid: a mosaic beyond 8×8 panels is almost certainly a
// wrong input (bad size units, absurd margin), and the capture assistant would be unusable anyway.
const MaxGridPerAxis = 8

// Warning codes returned in Plan.Warnings. Codes, not sentences: the UI translates them.
const (
	WarnGridClamped   = "grid_clamped"    // auto grid exceeded MaxGridPerAxis and was clamped
	WarnSizeUnknown   = "size_unknown"    // the object has no catalogued size → single-tile plan
	WarnTileCountHigh = "tile_count_high" // > 25 tiles: several nights of capture
)

// Tile capture statuses persisted per tile on a saved plan.
const (
	StatusPending  = "pending"
	StatusCaptured = "captured"
	StatusSkipped  = "skipped"
)

// Request are the pure inputs of a mosaic tile computation. Angles are degrees (position angles
// east of north, J2000), sizes arcminutes. The zero value of optional fields means "default":
// OverlapFrac 0 → 0.20, MarginArcmin < 0 → 10′, At zero → now.
type Request struct {
	RADeg  float64 `json:"ra_deg"`  // mosaic center (object center), J2000
	DecDeg float64 `json:"dec_deg"`

	// CenterRADeg/CenterDecDeg move the GRID off the object — hand-framing, set by dragging the
	// mosaic on the sky map when the interesting part of the field isn't centred on the catalogue
	// position (an extended nebula's bright lobe, a galaxy pair). The drag translates the grid
	// RIGIDLY: the tile count still comes from the object's own extent, so moving it re-frames
	// instead of silently growing the mosaic. HasCenter false = centre on the object.
	CenterRADeg  float64 `json:"center_ra_deg"`
	CenterDecDeg float64 `json:"center_dec_deg"`
	HasCenter    bool    `json:"has_center"`

	SizeArcmin      float64 `json:"size_arcmin"`       // object major axis; 0 = unknown → 1×1 grid
	SizeMinorArcmin float64 `json:"size_minor_arcmin"` // 0 = unknown → circle of SizeArcmin
	ObjectPADeg     float64 `json:"object_pa_deg"`     // orientation of the major axis
	HasObjectPA     bool    `json:"has_object_pa"`

	Optics skyplan.Optics `json:"optics"` // tile field of view derives from these

	OverlapFrac  float64 `json:"overlap_frac"`  // clamped to [0.05,0.5]
	MarginArcmin float64 `json:"margin_arcmin"` // sky beyond the object on each side
	CameraPADeg  float64 `json:"camera_pa_deg"` // ONE camera angle for the whole mosaic (EQ mount)
	RowsOverride int     `json:"rows_override"` // 0 = auto (rows span the sensor height axis)
	ColsOverride int     `json:"cols_override"` // 0 = auto (cols span the sensor width axis)

	Lat float64   `json:"lat"` // observer site, for the alt/az + transit enrichment
	Lon float64   `json:"lon"`
	At  time.Time `json:"at"`
}

// Tile is one pointing of the mosaic.
type Tile struct {
	Index  int    `json:"index"` // row*cols+col — stable identity for capture-status keys
	Row    int    `json:"row"`   // 0 = north/top row in the camera frame
	Col    int    `json:"col"`
	Order  int    `json:"order"`  // 1-based serpentine capture order
	Folder string `json:"folder"` // "p01"… — the capture-subfolder convention, pinned here only

	RADeg   float64       `json:"ra_deg"` // J2000, [0,360)
	DecDeg  float64       `json:"dec_deg"`
	Corners [4][2]float64 `json:"corners"` // [ra,dec] × TL,TR,BR,BL in frame orientation

	AltDeg       float64 `json:"alt_deg"` // at Request.At (snapshot; the UI re-derives live)
	AzDeg        float64 `json:"az_deg"`
	TransitUTCMs int64   `json:"transit_utc_ms"`
	MeridianSide string  `json:"meridian_side"` // "east" | "west"
}

// Grid describes the computed layout.
type Grid struct {
	Rows        int     `json:"rows"`
	Cols        int     `json:"cols"`
	TileWDeg    float64 `json:"tile_w_deg"`
	TileHDeg    float64 `json:"tile_h_deg"`
	StepWDeg    float64 `json:"step_w_deg"`
	StepHDeg    float64 `json:"step_h_deg"`
	CameraPADeg float64 `json:"camera_pa_deg"`
	OverlapFrac float64 `json:"overlap_frac"`
}

// Plan is the computed mosaic: the grid, its tiles in reading order (Index order), and warnings.
type Plan struct {
	Grid     Grid     `json:"grid"`
	Tiles    []Tile   `json:"tiles"`
	Warnings []string `json:"warnings,omitempty"`
}
