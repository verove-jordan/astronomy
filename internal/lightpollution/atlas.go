package lightpollution

import "github.com/verove-jordan/astronomy/internal/geogrid"

// atlasMeta is the JSON sidecar for the offline light-pollution grid. It is the shared geogrid.Meta (see
// internal/geogrid), aliased so the builder and coverage helpers here keep reading the same fields.
type atlasMeta = geogrid.Meta

// atlas is the offline light-pollution raster: the shared geogrid.Grid reader plus this package's SQM
// interpretation of its cell unit. nil when no atlas is installed.
type atlas struct {
	*geogrid.Grid
}

// loadAtlas opens the atlas at binPath plus its `<name>.json` sidecar, soft-failing to nil (no atlas)
// when either file is missing or malformed — the provider then skips the offline step.
func loadAtlas(binPath string) *atlas {
	g := geogrid.Load(binPath)
	if g == nil {
		return nil
	}
	return &atlas{Grid: g}
}

// sampleSQM bilinearly samples the grid at (lat, lon) and converts the raw cell value (in Meta.Unit) to
// SQM. It returns ok=false when the point is outside coverage or all four neighbours are nodata.
func (a *atlas) sampleSQM(lat, lon float64) (float64, bool) {
	v, ok := a.SampleBilinear(lat, lon)
	if !ok {
		return 0, false
	}
	return valueToSQM(v, a.Meta.Unit), true
}
