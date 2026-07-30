package mosaicplan

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// Hand-framing (dragging the mosaic on the sky map) re-frames rather than re-plans: the grid keeps
// its shape and tile spacing and simply sits around the new centre. Note it is NOT an exactly rigid
// translation — the tiles are re-laid-out in the tangent plane of the NEW centre, so gnomonic
// distortion shifts them by a fraction of an arcminute relative to a pure shift. That is the
// correct behaviour (tile spacing stays right around the point actually being shot).
func TestCompute_HandFramedCenterMovesTheGrid(t *testing.T) {
	base, err := Compute(m31Request())
	require.NoError(t, err)

	req := m31Request()
	// Move the grid half a degree east and a quarter degree north of the object.
	req.CenterRADeg, req.CenterDecDeg, req.HasCenter = tangentOffset(req.RADeg, req.DecDeg, 0.5, 0.25)
	moved, err := Compute(req)
	require.NoError(t, err)

	require.Equal(t, base.Grid.Rows, moved.Grid.Rows, "moving the grid must not change its shape")
	require.Equal(t, base.Grid.Cols, moved.Grid.Cols)
	require.Len(t, moved.Tiles, len(base.Tiles))
	assert.InDelta(t, base.Grid.StepWDeg, moved.Grid.StepWDeg, 1e-12)
	assert.InDelta(t, base.Grid.StepHDeg, moved.Grid.StepHDeg, 1e-12)

	// The tile centroid now sits on the hand-framed centre, not on the object.
	var sumXi, sumEta float64
	for _, tile := range moved.Tiles {
		xi, eta, ok := astro.TangentPlane(req.CenterRADeg, req.CenterDecDeg, tile.RADeg, tile.DecDeg)
		require.True(t, ok)
		sumXi += xi
		sumEta += eta
	}
	assert.InDelta(t, 0, sumXi, 1e-9)
	assert.InDelta(t, 0, sumEta, 1e-9)

	// Every tile followed the drag (within gnomonic distortion over a ~2° field).
	for i := range base.Tiles {
		xi, eta, ok := astro.TangentPlane(base.Tiles[i].RADeg, base.Tiles[i].DecDeg,
			moved.Tiles[i].RADeg, moved.Tiles[i].DecDeg)
		require.True(t, ok)
		assert.InDelta(t, 0.5, xi, 0.02, "tile %d ξ offset", i)
		assert.InDelta(t, 0.25, eta, 0.02, "tile %d η offset", i)
	}

	// Neighbouring tiles still overlap by the planned amount.
	sep := astro.AngularSeparation(moved.Tiles[0].RADeg, moved.Tiles[0].DecDeg,
		moved.Tiles[1].RADeg, moved.Tiles[1].DecDeg)
	assert.InDelta(t, moved.Grid.StepWDeg, sep, moved.Grid.StepWDeg*0.01)
}

func TestCompute_CenterDefaultsToTheObject(t *testing.T) {
	req := m31Request()
	require.False(t, req.HasCenter)
	ra, dec := GridCenter(req)
	assert.Equal(t, req.RADeg, ra)
	assert.Equal(t, req.DecDeg, dec)

	// An explicit centre equal to the object plans identically to no centre at all.
	req.CenterRADeg, req.CenterDecDeg, req.HasCenter = req.RADeg, req.DecDeg, true
	withCenter, err := Compute(req)
	require.NoError(t, err)
	plain, err := Compute(m31Request())
	require.NoError(t, err)
	assert.True(t, SameGeometry(plain, withCenter))
}

// A moved grid is a DIFFERENT layout, so SameGeometry must report false — that is what makes the
// API reset per-tile capture progress instead of silently re-pointing tiles the user already shot.
func TestSameGeometry_DetectsAMovedCenter(t *testing.T) {
	plain, err := Compute(m31Request())
	require.NoError(t, err)

	req := m31Request()
	req.CenterRADeg, req.CenterDecDeg, req.HasCenter = tangentOffset(req.RADeg, req.DecDeg, 0.2, 0)
	moved, err := Compute(req)
	require.NoError(t, err)

	assert.False(t, SameGeometry(plain, moved))
}

// tangentOffset returns the sky position xi/eta degrees (east/north) from ra/dec, plus the
// has-centre flag, so tests can express a drag in the same tangent-plane units the UI uses.
func tangentOffset(ra, dec, xi, eta float64) (float64, float64, bool) {
	outRA, outDec := astro.TangentSky(ra, dec, xi, eta)
	if math.IsNaN(outRA) || math.IsNaN(outDec) {
		panic("bad tangent offset")
	}
	return outRA, outDec, true
}
