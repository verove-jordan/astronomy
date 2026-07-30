package fits

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCanvasWCS(t *testing.T) WCS {
	t.Helper()
	// North-up, east-left at ~1.06″/px: CD1_1 negative (RA grows leftward), CD2_2 positive.
	scale := 1.06 / 3600
	w, ok := NewTanWCS(10.684, 41.269, 8.5, 8.5, [2][2]float64{{-scale, 0}, {0, scale}})
	require.True(t, ok)
	return w
}

func TestNewTanWCS_SingularRejected(t *testing.T) {
	_, ok := NewTanWCS(0, 0, 1, 1, [2][2]float64{{1e-4, 0}, {2e-4, 0}})
	assert.False(t, ok)
}

func TestNewTanWCS_ProjectsAboutReferencePixel(t *testing.T) {
	w := testCanvasWCS(t)
	// The tangent point lands on the reference pixel (1-based 8.5 → 0-based 7.5).
	x, y, ok := w.SkyToPix(10.684, 41.269)
	require.True(t, ok)
	assert.InDelta(t, 7.5, x, 1e-9)
	assert.InDelta(t, 7.5, y, 1e-9)
	// Round trip through the inverse.
	ra, dec := w.PixToSky(3.25, 11.75)
	x, y, ok = w.SkyToPix(ra, dec)
	require.True(t, ok)
	assert.InDelta(t, 3.25, x, 1e-9)
	assert.InDelta(t, 11.75, y, 1e-9)
}

func TestWriteFITSWith_WCSRoundTrip(t *testing.T) {
	w := testCanvasWCS(t)
	im := NewImage(16, 16, 1)
	im.Pix[0][5*16+4] = 0.75
	path := filepath.Join(t.TempDir(), "canvas.fits")
	require.NoError(t, im.WriteFITSWith(path, w.Cards()))

	f, err := Open(path)
	require.NoError(t, err)
	got, ok := ParseWCS(f.Header)
	require.True(t, ok, "written cards must parse back as a TAN solution")
	assert.InDelta(t, w.RA0, got.RA0, 1e-12)
	assert.InDelta(t, w.Dec0, got.Dec0, 1e-12)
	assert.InDelta(t, w.CRPix1, got.CRPix1, 1e-12)
	assert.InDelta(t, w.CRPix2, got.CRPix2, 1e-12)
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			assert.InDelta(t, w.CD[i][j], got.CD[i][j], 1e-18)
		}
	}
	assert.InDelta(t, 1.06, got.ScaleArcsecPerPix(), 1e-9)

	// Pixel data survives the extra cards.
	back, err := ReadImage(path)
	require.NoError(t, err)
	assert.InDelta(t, 0.75, float64(back.Pix[0][5*16+4]), 1e-6)
}

func TestWriteFITS_PlainHasNoWCS(t *testing.T) {
	im := NewImage(8, 8, 1)
	path := filepath.Join(t.TempDir(), "plain.fits")
	require.NoError(t, im.WriteFITS(path))
	f, err := Open(path)
	require.NoError(t, err)
	_, ok := ParseWCS(f.Header)
	assert.False(t, ok)
}
