// Package mosaicplan computes mosaic capture plans: given an object (center, ellipse, position
// angle), the imaging optics and an overlap fraction, it lays out a grid of overlapping camera
// tiles in the tangent plane at the object center and returns each tile's pointing (RA/Dec),
// footprint corners, capture order and site-time enrichment (alt/az, transit, meridian side).
//
// Conventions (shared with the mosaic assembler): position angles are degrees EAST OF NORTH,
// J2000; tangent-plane standard coordinates ξ (east-positive) / η (north-positive) in degrees,
// identical to internal/fits/tan.go; the camera frame axis u is the sensor width, v the height,
// so PA=0 puts v along celestial north. All tile corners project from the ONE tangent point at
// the mosaic center — the same projection the assembler's canvas uses.
package mosaicplan

import (
	"fmt"
	"time"
)

const (
	defaultOverlapFrac  = 0.20
	minOverlapFrac      = 0.05
	maxOverlapFrac      = 0.50
	defaultMarginArcmin = 10
	highTileCount       = 25
)

// Compute validates req, applies defaults and returns the tile plan. It errors only on inputs no
// default can repair (optics that yield no field of view); geometry surprises surface as warnings.
func Compute(req Request) (Plan, error) {
	req = applyDefaults(req)
	fovW, fovH := req.Optics.FOV()
	if fovW <= 0 || fovH <= 0 {
		return Plan{}, fmt.Errorf("optics yield no field of view (focal %.0f mm, pixel %.2f µm, sensor %dx%d)",
			req.Optics.FocalMM, req.Optics.PixelUm, req.Optics.SensorWpx, req.Optics.SensorHpx)
	}

	grid, warnings := gridFor(req, fovW, fovH)
	tiles := tilesFor(req, grid)
	orderTiles(tiles, grid)
	enrichTiles(tiles, req)
	if len(tiles) > highTileCount {
		warnings = append(warnings, WarnTileCountHigh)
	}
	return Plan{Grid: grid, Tiles: tiles, Warnings: warnings}, nil
}

func applyDefaults(req Request) Request {
	if req.OverlapFrac == 0 {
		req.OverlapFrac = defaultOverlapFrac
	}
	req.OverlapFrac = clampF(req.OverlapFrac, minOverlapFrac, maxOverlapFrac)
	if req.MarginArcmin < 0 {
		req.MarginArcmin = defaultMarginArcmin
	}
	if req.At.IsZero() {
		req.At = time.Now().UTC()
	}
	return req
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
