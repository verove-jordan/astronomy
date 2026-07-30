package mosaicplan

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// gridFor sizes the grid: the object ellipse (semi-axes a=major/2, b=minor/2, degrees) is rotated
// into the camera frame, margins are added, and each axis is covered by tiles advancing one
// step = FOV·(1−overlap) at a time.
func gridFor(req Request, fovW, fovH float64) (Grid, []string) {
	var warnings []string
	stepW := fovW * (1 - req.OverlapFrac)
	stepH := fovH * (1 - req.OverlapFrac)

	halfU, halfV := extentHalf(req)
	if req.SizeArcmin <= 0 {
		warnings = append(warnings, WarnSizeUnknown)
	}
	margin := req.MarginArcmin / 60
	extentU := 2*halfU + 2*margin
	extentV := 2*halfV + 2*margin

	cols := autoCount(extentU, fovW, stepW)
	rows := autoCount(extentV, fovH, stepH)
	if req.ColsOverride > 0 {
		cols = req.ColsOverride
	}
	if req.RowsOverride > 0 {
		rows = req.RowsOverride
	}
	if cols > MaxGridPerAxis || rows > MaxGridPerAxis {
		cols = minInt(cols, MaxGridPerAxis)
		rows = minInt(rows, MaxGridPerAxis)
		warnings = append(warnings, WarnGridClamped)
	}
	return Grid{
		Rows: rows, Cols: cols,
		TileWDeg: fovW, TileHDeg: fovH,
		StepWDeg: stepW, StepHDeg: stepH,
		CameraPADeg: req.CameraPADeg,
		OverlapFrac: req.OverlapFrac,
	}, warnings
}

// extentHalf returns the object's half-extents along the camera frame axes (u=width, v=height),
// in degrees. With a known position angle the true ellipse is rotated by Δ = objectPA − cameraPA;
// without one the orientation is unknown, so the safe cover is the circle of the major axis.
func extentHalf(req Request) (halfU, halfV float64) {
	a := req.SizeArcmin / 120 // semi-major, degrees
	b := req.SizeMinorArcmin / 120
	if b <= 0 || b > a {
		b = a
	}
	if !req.HasObjectPA {
		return a, a
	}
	sinD, cosD := math.Sincos((req.ObjectPADeg - req.CameraPADeg) * math.Pi / 180)
	halfU = math.Sqrt(a*a*sinD*sinD + b*b*cosD*cosD)
	halfV = math.Sqrt(a*a*cosD*cosD + b*b*sinD*sinD)
	return halfU, halfV
}

// autoCount is the number of tiles needed to span extent with tile-sized windows advancing by
// step: 1 when a single tile covers it, else 1 + the extra steps to reach the far edge.
func autoCount(extent, tile, step float64) int {
	if extent <= tile {
		return 1
	}
	return 1 + int(math.Ceil((extent-tile)/step))
}

// tilesFor lays the rows×cols tile centers and footprint corners out on the sky. Offsets are
// computed in the camera frame (u right along the sensor width, v up along the height, row 0 at
// the top/north), rotated by the camera PA, and inverse-projected from the single tangent point
// at the mosaic center.
func tilesFor(req Request, grid Grid) []Tile {
	tiles := make([]Tile, 0, grid.Rows*grid.Cols)
	for row := 0; row < grid.Rows; row++ {
		for col := 0; col < grid.Cols; col++ {
			u := (float64(col) - float64(grid.Cols-1)/2) * grid.StepWDeg
			v := (float64(grid.Rows-1)/2 - float64(row)) * grid.StepHDeg
			ra, dec := frameToSky(req, u, v)
			t := Tile{
				Index: row*grid.Cols + col,
				Row:   row, Col: col,
				RADeg: ra, DecDeg: dec,
			}
			halfW, halfH := grid.TileWDeg/2, grid.TileHDeg/2
			cornerUV := [4][2]float64{ // TL, TR, BR, BL in frame orientation
				{u - halfW, v + halfH},
				{u + halfW, v + halfH},
				{u + halfW, v - halfH},
				{u - halfW, v - halfH},
			}
			for i, c := range cornerUV {
				cra, cdec := frameToSky(req, c[0], c[1])
				t.Corners[i] = [2]float64{cra, cdec}
			}
			tiles = append(tiles, t)
		}
	}
	return tiles
}

// frameToSky rotates a camera-frame offset (u,v) by the camera position angle into tangent-plane
// standard coordinates and inverse-projects it from the mosaic center. The rotation follows the
// pinned convention: ξ = u·cosPA + v·sinPA, η = −u·sinPA + v·cosPA.
func frameToSky(req Request, u, v float64) (raDeg, decDeg float64) {
	sinPA, cosPA := math.Sincos(req.CameraPADeg * math.Pi / 180)
	xi := u*cosPA + v*sinPA
	eta := -u*sinPA + v*cosPA
	cra, cdec := GridCenter(req)
	return astro.TangentSky(cra, cdec, xi, eta)
}

// GridCenter is the tangent point the tiles are laid out around: the hand-framed centre when the
// user has dragged the mosaic, else the object itself.
func GridCenter(req Request) (raDeg, decDeg float64) {
	if req.HasCenter {
		return req.CenterRADeg, req.CenterDecDeg
	}
	return req.RADeg, req.DecDeg
}

// SameGeometry reports whether two plans describe the same tile layout (grid shape, camera angle,
// overlap, and every tile center within ~4 mas). Site/time enrichment is deliberately ignored —
// re-saving a plan at a different hour must NOT count as a geometry change (which would reset the
// per-tile capture progress).
func SameGeometry(a, b Plan) bool {
	if a.Grid.Rows != b.Grid.Rows || a.Grid.Cols != b.Grid.Cols {
		return false
	}
	if math.Abs(a.Grid.CameraPADeg-b.Grid.CameraPADeg) > 1e-6 ||
		math.Abs(a.Grid.OverlapFrac-b.Grid.OverlapFrac) > 1e-6 {
		return false
	}
	if len(a.Tiles) != len(b.Tiles) {
		return false
	}
	for i := range a.Tiles {
		if math.Abs(a.Tiles[i].RADeg-b.Tiles[i].RADeg) > 1e-6 ||
			math.Abs(a.Tiles[i].DecDeg-b.Tiles[i].DecDeg) > 1e-6 {
			return false
		}
	}
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
