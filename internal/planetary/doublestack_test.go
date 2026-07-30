package planetary

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestDenseGridN(t *testing.T) {
	assert.Equal(t, 29, denseGridN(4656, 3520), "ASI 1600 frame → ~120 px cells")
	assert.Equal(t, apGridN, denseGridN(500, 500), "never coarser than pass 1's grid")
	assert.Equal(t, apDenseGridMax, denseGridN(8000, 8000), "capped")
}

func TestAlignPointsGridN(t *testing.T) {
	tests := []struct {
		total int
		want  int
	}{
		{1, apGridN}, {100, 10}, {500, 22}, {2304, 48}, {9999, apAlignPointsGridMax},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.total), func(t *testing.T) {
			assert.Equal(t, tt.want, alignPointsGridN(tt.total))
		})
	}
}

func TestSnapAlignPoints(t *testing.T) {
	tests := []struct {
		v    int
		want int
	}{
		{0, 0}, {-5, 0}, {99, 100}, {100, 100}, {500, 484}, {2304, 2304}, {2305, 2304},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.v), func(t *testing.T) {
			assert.Equal(t, tt.want, SnapAlignPoints(tt.v), "snaps to N² of the clamped per-axis density")
		})
	}
}

func TestDenseGridNFor(t *testing.T) {
	// 0 = auto reproduces the frame-size formula exactly; an override is absolute (ignores the frame).
	assert.Equal(t, 29, denseGridNFor(4656, 3520, 0), "auto = denseGridN")
	assert.Equal(t, apDenseGridMax, denseGridNFor(8000, 8000, 0), "auto stays capped at 32")
	assert.Equal(t, 48, denseGridNFor(4656, 3520, 2304), "explicit override reaches 48")
	assert.Equal(t, 30, denseGridNFor(500, 500, 900), "override ignores frame size")
	assert.Equal(t, apGridN, denseGridNFor(500, 500, 0), "small frame auto floors at 10")
}

func TestWarpToMaster_ConvergesOntoTheReference(t *testing.T) {
	dir := t.TempDir()
	master0 := texturedDisk(256, 256) // stands in for the pass-1 stacked master
	// Frames = globally shifted copies of the master; their pass-1 fields carry the true shift.
	// Pass 2 must land every frame back onto the master's geometry.
	shifts := []struct{ dx, dy float64 }{{2.5, -1.5}, {-3, 2}}
	var paths []string
	var dxF, dyF [][]float64
	for i, s := range shifts {
		im := comet.Translate(master0, s.dx, s.dy)
		p := filepath.Join(dir, fmt.Sprintf("src_%d.fits", i))
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
		// comet.Translate(ref, +T) moves content by +T, and warpByGrid samples at x−field — so the
		// pass-1 field that aligns the frame back is −T (verified: seeding +T lands 2T off, ~4× ssd).
		dx, dy := uniformGrid(-s.dx, -s.dy, apGridN) // the 10×10 shape pass 1 produces
		dxF, dyF = append(dxF, dx), append(dyF, dy)
	}

	res, err := warpToMaster(context.Background(), master0, paths, dxF, dyF, dir, "d", 1, denseGridN(master0.W, master0.H), nil)
	require.NoError(t, err)
	require.Len(t, res.paths, 2)
	require.Len(t, res.cellSharp, 2, "dense-grid local sharpness measured for the re-stack")
	for i, p := range res.paths {
		src, err := fits.ReadImage(paths[i])
		require.NoError(t, err)
		warped, err := fits.ReadImage(p)
		require.NoError(t, err)
		assert.Less(t, ssd(master0, warped), ssd(master0, src)*0.5,
			"re-registered frame %d converges onto the master's geometry", i)
	}
}
