package mosaicplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// fc100asi1600 is the reference rig: FC-100 DF (740 mm) + ASI 1600MM Pro (3.8 µm, 4656×3520)
// → 1.06″/px, 1.37° × 1.04° field.
var fc100asi1600 = skyplan.Optics{FocalMM: 740, ApertureMM: 100, PixelUm: 3.8, SensorWpx: 4656, SensorHpx: 3520}

// m31Request is the plan-file golden: M31 (178′×63′, PA 35°), camera aligned to the object
// (PA = 35+90 = 125°), 20 % overlap, 10′ margin → a 3×2 grid.
func m31Request() Request {
	return Request{
		RADeg: 10.6847, DecDeg: 41.2687,
		SizeArcmin: 178, SizeMinorArcmin: 63,
		ObjectPADeg: 35, HasObjectPA: true,
		Optics:      fc100asi1600,
		OverlapFrac: 0.2, MarginArcmin: 10, CameraPADeg: 125,
		Lat: 48.3, Lon: 2.7, At: time.Date(2026, 8, 15, 22, 0, 0, 0, time.UTC),
	}
}

func TestCompute_M31Golden(t *testing.T) {
	plan, err := Compute(m31Request())
	require.NoError(t, err)

	assert.Equal(t, 3, plan.Grid.Cols, "M31 major axis spans 3 tiles across the sensor width")
	assert.Equal(t, 2, plan.Grid.Rows)
	require.Len(t, plan.Tiles, 6)
	assert.Empty(t, plan.Warnings)
	assert.InDelta(t, 1.370, plan.Grid.TileWDeg, 0.005)
	assert.InDelta(t, 1.036, plan.Grid.TileHDeg, 0.005)

	// Adjacent same-row tiles sit one step apart on the sky (< the tile width ⇒ they overlap).
	sep := astro.AngularSeparation(plan.Tiles[0].RADeg, plan.Tiles[0].DecDeg,
		plan.Tiles[1].RADeg, plan.Tiles[1].DecDeg)
	assert.InDelta(t, plan.Grid.StepWDeg, sep, plan.Grid.StepWDeg*0.01)
	assert.Less(t, sep, plan.Grid.TileWDeg)

	// The grid is centered on the object: tangent-plane centroid of the tile centers is zero.
	var sumXi, sumEta float64
	for _, tile := range plan.Tiles {
		xi, eta, ok := astro.TangentPlane(10.6847, 41.2687, tile.RADeg, tile.DecDeg)
		require.True(t, ok)
		sumXi += xi
		sumEta += eta
	}
	assert.InDelta(t, 0, sumXi, 1e-9)
	assert.InDelta(t, 0, sumEta, 1e-9)

	// Corner sanity: each tile's top edge spans the tile width, its left edge the tile height —
	// whatever the camera rotation.
	for _, tile := range plan.Tiles {
		top := astro.AngularSeparation(tile.Corners[0][0], tile.Corners[0][1], tile.Corners[1][0], tile.Corners[1][1])
		left := astro.AngularSeparation(tile.Corners[0][0], tile.Corners[0][1], tile.Corners[3][0], tile.Corners[3][1])
		assert.InDelta(t, plan.Grid.TileWDeg, top, plan.Grid.TileWDeg*0.01)
		assert.InDelta(t, plan.Grid.TileHDeg, left, plan.Grid.TileHDeg*0.01)
	}
}

func TestCompute_SmallObjectDegeneratesToSingleTile(t *testing.T) {
	req := m31Request()
	req.SizeArcmin, req.SizeMinorArcmin = 10, 8
	plan, err := Compute(req)
	require.NoError(t, err)
	assert.Equal(t, 1, plan.Grid.Rows)
	assert.Equal(t, 1, plan.Grid.Cols)
	require.Len(t, plan.Tiles, 1)
	assert.InDelta(t, req.RADeg, plan.Tiles[0].RADeg, 1e-9, "single tile sits on the object center")
	assert.InDelta(t, req.DecDeg, plan.Tiles[0].DecDeg, 1e-9)
}

func TestCompute_UnknownSizeWarnsAndCoversCenter(t *testing.T) {
	req := m31Request()
	req.SizeArcmin, req.SizeMinorArcmin, req.HasObjectPA = 0, 0, false
	plan, err := Compute(req)
	require.NoError(t, err)
	assert.Contains(t, plan.Warnings, WarnSizeUnknown)
	assert.Len(t, plan.Tiles, 1)
}

func TestCompute_NearPoleAndRAWrap(t *testing.T) {
	tests := []struct {
		name           string
		ra, dec        float64
		wantRAStraddle bool
	}{
		{"near pole", 10, 88, false},
		{"ra wrap", 359.9, 20, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := m31Request()
			req.RADeg, req.DecDeg = tt.ra, tt.dec
			plan, err := Compute(req)
			require.NoError(t, err)
			var below, above int
			for _, tile := range plan.Tiles {
				assert.GreaterOrEqual(t, tile.RADeg, 0.0)
				assert.Less(t, tile.RADeg, 360.0)
				assert.False(t, tile.DecDeg > 90 || tile.DecDeg < -90)
				for _, c := range plan.Tiles[0].Corners {
					assert.False(t, c[1] > 90 || c[1] < -90)
				}
				if tile.RADeg < 180 {
					below++
				} else {
					above++
				}
			}
			if tt.wantRAStraddle {
				assert.Positive(t, below, "tiles east of the wrap")
				assert.Positive(t, above, "tiles west of the wrap")
			}
		})
	}
}

func TestCompute_CameraPASwapsAxes(t *testing.T) {
	base := m31Request()
	base.SizeArcmin, base.SizeMinorArcmin, base.ObjectPADeg = 100, 40, 0
	base.MarginArcmin = 0

	base.CameraPADeg = 0
	pa0, err := Compute(base)
	require.NoError(t, err)
	base.CameraPADeg = 90
	pa90, err := Compute(base)
	require.NoError(t, err)

	assert.Equal(t, pa0.Grid.Rows, pa90.Grid.Cols, "rotating the camera 90° swaps the grid axes")
	assert.Equal(t, pa0.Grid.Cols, pa90.Grid.Rows)
}

func TestCompute_GridClampAndOverrides(t *testing.T) {
	req := m31Request()
	req.SizeArcmin, req.SizeMinorArcmin = 1800, 1800 // 30° "object"
	plan, err := Compute(req)
	require.NoError(t, err)
	assert.Equal(t, MaxGridPerAxis, plan.Grid.Rows)
	assert.Equal(t, MaxGridPerAxis, plan.Grid.Cols)
	assert.Contains(t, plan.Warnings, WarnGridClamped)
	assert.Contains(t, plan.Warnings, WarnTileCountHigh)

	req = m31Request()
	req.RowsOverride, req.ColsOverride = 2, 5
	plan, err = Compute(req)
	require.NoError(t, err)
	assert.Equal(t, 2, plan.Grid.Rows)
	assert.Equal(t, 5, plan.Grid.Cols)
}

func TestCompute_BadOpticsRejected(t *testing.T) {
	req := m31Request()
	req.Optics = skyplan.Optics{}
	_, err := Compute(req)
	assert.Error(t, err)
}

func TestCompute_MeridianSideMatchesHourAngle(t *testing.T) {
	plan, err := Compute(m31Request())
	require.NoError(t, err)
	req := m31Request()
	for _, tile := range plan.Tiles {
		want := "east"
		if astro.HourAngleDeg(tile.RADeg, req.Lon, req.At) > 0 {
			want = "west"
		}
		assert.Equal(t, want, tile.MeridianSide)
		assert.NotZero(t, tile.TransitUTCMs)
	}
}

func TestSameGeometry_IgnoresEnrichment(t *testing.T) {
	a, err := Compute(m31Request())
	require.NoError(t, err)
	req := m31Request()
	req.At = req.At.Add(26 * time.Hour) // different night → different alt/az/transit
	b, err := Compute(req)
	require.NoError(t, err)
	assert.True(t, SameGeometry(a, b), "site/time enrichment must not count as geometry")

	req.OverlapFrac = 0.3
	c, err := Compute(req)
	require.NoError(t, err)
	assert.False(t, SameGeometry(a, c))
}
