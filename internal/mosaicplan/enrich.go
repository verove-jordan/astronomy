package mosaicplan

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// enrichTiles stamps each tile with its site-time context at req.At: alt/az snapshot, the nearest
// transit, and which side of the meridian it sits on (positive hour angle = past transit = west).
// The UI re-derives alt/az live; these snapshots make the API response self-sufficient (curl,
// run.json provenance).
func enrichTiles(tiles []Tile, req Request) {
	for i := range tiles {
		t := &tiles[i]
		alt, az := astro.Horizontal(t.RADeg, t.DecDeg, req.Lat, req.Lon, req.At)
		t.AltDeg = round2(alt)
		t.AzDeg = round2(az)
		t.TransitUTCMs = astro.TransitTimeUTC(t.RADeg, req.Lon, req.At).UnixMilli()
		if astro.HourAngleDeg(t.RADeg, req.Lon, req.At) > 0 {
			t.MeridianSide = "west"
		} else {
			t.MeridianSide = "east"
		}
	}
}

func round2(x float64) float64 {
	return math.Round(x*100) / 100
}
