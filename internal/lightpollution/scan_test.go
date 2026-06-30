package lightpollution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

func uniformCells(n int, v float32) []float32 {
	cells := make([]float32, n)
	for i := range cells {
		cells[i] = v
	}
	return cells
}

func TestScanArea_SamplesAtlasGrid(t *testing.T) {
	bin := writeAtlas(t, atlasMeta{
		Rows: 4, Cols: 4, LatMin: 40, LatMax: 50, LonMin: 0, LonMax: 10, Unit: "sqm", NoData: -1,
	}, uniformCells(16, 21.5))
	p := New(&config.Config{WorkDir: t.TempDir(), LightPollutionAtlas: bin, LightPollutionTileURL: ""})

	cells := p.ScanArea(context.Background(), Bbox{MinLat: 42, MinLon: 2, MaxLat: 48, MaxLon: 8}, 5, 5)
	require.Len(t, cells, 25)
	for _, c := range cells {
		assert.InDelta(t, 21.5, c.SQM, 0.01)
		assert.Equal(t, sqmToBortle(21.5), c.Bortle)
	}
}

func TestScanArea_SkipsCellsOutsideCoverage(t *testing.T) {
	bin := writeAtlas(t, atlasMeta{
		Rows: 4, Cols: 4, LatMin: 40, LatMax: 50, LonMin: 0, LonMax: 10, Unit: "sqm", NoData: -1,
	}, uniformCells(16, 21.0))
	// Tiles disabled, so cells beyond the atlas's lon range (>10°) resolve to nothing and are skipped.
	p := New(&config.Config{WorkDir: t.TempDir(), LightPollutionAtlas: bin, LightPollutionTileURL: ""})

	cells := p.ScanArea(context.Background(), Bbox{MinLat: 42, MinLon: 5, MaxLat: 48, MaxLon: 20}, 5, 5)
	assert.Greater(t, len(cells), 0)
	assert.Less(t, len(cells), 25)
}
