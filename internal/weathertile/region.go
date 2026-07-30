package weathertile

import "math"

// regionBlockBits picks how many tiles (2^bits per side) share one weather cube at zoom z. A cube covers at
// most ~48° (the provider clamps its radius to 24°), so the block must stay within that: full 8×8 blocks
// once tiles are small (z≥6), shrinking toward a single tile when zoomed far out.
func regionBlockBits(z int) int {
	if z >= 6 {
		return 3 // 8×8
	}
	if z <= 3 {
		return 0 // 1×1
	}
	return z - 3
}

// regionZoomCap folds every zoom deeper than this onto its z8 ancestor block: past z8 the forecast grid
// has no finer data to offer (the provider's step floor ≈ Open-Meteo's native resolution), so deeper
// zooms would only mint new region scales — each a fresh multi-hundred-point cube fetch per zoom level,
// which is exactly the request volume that starved the free-tier quota.
const regionZoomCap = 8

// TileRegion returns the shared weather-cube region (centre lat/lon + radius°) for map tile (z,x,y): the
// tile is snapped to a 2^bits block so all tiles in the block resolve to ONE cube (one cached fetch,
// continuous across tile edges) and the radius spans the block. Adjacent blocks overlap via the provider's
// fetch margin, so tile edges stay seamless. This is what makes a whole viewport reuse one cached cube.
// The frames endpoint anchors to the SAME quantizer (LatLonToTile + TileRegion), so its cube and the
// tiles' cube share one cache key — one upstream fetch serves the scrubber axis and every metric.
func TileRegion(z, x, y int) (centerLat, centerLon, radiusDeg float64) {
	if z < 0 {
		z = 0
	}
	if z > regionZoomCap {
		shift := z - regionZoomCap
		x, y, z = x>>shift, y>>shift, regionZoomCap
	}
	block := 1 << regionBlockBits(z)
	rx := (x / block) * block
	ry := (y / block) * block
	lonW := tile2lon(rx, z)
	lonE := tile2lon(rx+block, z)
	latN := tile2lat(ry, z)
	latS := tile2lat(ry+block, z)
	centerLon = (lonW + lonE) / 2
	centerLat = (latN + latS) / 2
	radiusDeg = math.Max(math.Abs(lonE-lonW), math.Abs(latN-latS)) / 2
	return centerLat, centerLon, radiusDeg
}

// LatLonToTile is the slippy-map inverse of tile2lon/tile2lat: the (x,y) of the tile containing
// (lat,lon) at zoom z, clamped to the valid range. The frames endpoint uses it to resolve the SAME
// block region (and so the same cube cache key) as the tiles the map is about to request.
func LatLonToTile(lat, lon float64, z int) (x, y int) {
	if z < 0 {
		z = 0
	}
	// Web-Mercator's pole singularity: clamp to its latitude limit so tan/cos stay finite.
	if lat > 85.0511 {
		lat = 85.0511
	} else if lat < -85.0511 {
		lat = -85.0511
	}
	n := math.Exp2(float64(z))
	x = int(math.Floor((lon + 180) / 360 * n))
	latRad := lat * math.Pi / 180
	y = int(math.Floor((1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n))
	limit := int(n) - 1
	if x < 0 {
		x = 0
	} else if x > limit {
		x = limit
	}
	if y < 0 {
		y = 0
	} else if y > limit {
		y = limit
	}
	return x, y
}

func tile2lon(x, z int) float64 {
	return float64(x)/math.Exp2(float64(z))*360 - 180
}

func tile2lat(y, z int) float64 {
	n := math.Pi * (1 - 2*float64(y)/math.Exp2(float64(z)))
	return 180 / math.Pi * math.Atan(math.Sinh(n))
}

// FrameIndex returns the index of timeMs in timesteps (exact match preferred, else the nearest frame), or
// -1 when there are no timesteps.
func FrameIndex(timesteps []int64, timeMs int64) int {
	if len(timesteps) == 0 {
		return -1
	}
	best, bestDiff := 0, int64(math.MaxInt64)
	for i, t := range timesteps {
		if t == timeMs {
			return i
		}
		d := t - timeMs
		if d < 0 {
			d = -d
		}
		if d < bestDiff {
			best, bestDiff = i, d
		}
	}
	return best
}
