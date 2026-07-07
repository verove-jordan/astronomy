package optics

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

func TestAnalyzeFlatSaturated(t *testing.T) {
	dir := t.TempDir()
	master := fitstest.Write(t, dir, "master.fits", 64, 64, 30000, nil)
	rawSat := fitstest.Write(t, dir, "raw_sat.fits", 64, 64, 65300, nil) // clipped

	qc, _, err := AnalyzeFlat(master, []string{rawSat})
	require.NoError(t, err)
	assert.Equal(t, "bad", qc.Status, "a clipped raw flat is unusable")
	assert.Greater(t, qc.SaturFrac, saturBad)
	assert.NotEmpty(t, qc.Notes)
}

func TestAnalyzeFlatDim(t *testing.T) {
	dir := t.TempDir()
	master := fitstest.Write(t, dir, "dim.fits", 64, 64, 2000, nil) // ~3% of full scale

	qc, _, err := AnalyzeFlat(master, nil)
	require.NoError(t, err)
	assert.Contains(t, []string{"warn", "bad"}, qc.Status)
	assert.Less(t, qc.Level, levelWarnLo)
}

func TestAnalyzeFlatGood(t *testing.T) {
	dir := t.TempDir()
	master := fitstest.Write(t, dir, "good.fits", 64, 64, 30000, nil) // ~46% of full scale
	rawGood := fitstest.Write(t, dir, "raw_good.fits", 64, 64, 30000, nil)

	qc, defects, err := AnalyzeFlat(master, []string{rawGood})
	require.NoError(t, err)
	assert.Equal(t, "ok", qc.Status)
	assert.Empty(t, defects)
	assert.InDelta(t, 30000.0/65535.0, qc.Level, 0.01)
	assert.Zero(t, qc.SaturFrac)
}

func TestAnalyzeFlatNoRawFlats(t *testing.T) {
	dir := t.TempDir()
	master := fitstest.Write(t, dir, "m.fits", 64, 64, 30000, nil)

	qc, _, err := AnalyzeFlat(master, nil)
	require.NoError(t, err)
	assert.Zero(t, qc.SaturFrac)
	assert.Contains(t, qc.Notes, "no raw flats to check saturation")
}

// TestLoadFlatPlaneBayer confirms the RGGB superpixel-mean path cancels the CFA checkerboard, so a
// Bayer master produces no spurious defects.
func TestLoadFlatPlaneBayer(t *testing.T) {
	dir := t.TempDir()
	const w, h = 64, 64
	pix := make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v uint16
			switch {
			case x%2 == 0 && y%2 == 0:
				v = 20000 // R
			case x%2 == 1 && y%2 == 1:
				v = 15000 // B
			default:
				v = 30000 // G
			}
			pix[y*w+x] = v
		}
	}
	path := filepath.Join(dir, "bayer.fits")
	require.Equal(t, path, fitstest.WritePixels(t, dir, "bayer.fits", w, h, pix, map[string]string{"BAYERPAT": "'RGGB'"}))

	plane, pw, ph, scale, bayer, err := LoadFlatPlane(path)
	require.NoError(t, err)
	assert.True(t, bayer)
	assert.Equal(t, 2.0, scale, "superpixel factor")
	assert.Equal(t, w/2, pw)
	assert.Equal(t, h/2, ph)

	defects, _ := DetectDefects(plane, pw, ph, scale)
	assert.Empty(t, defects, "the RGGB pattern must not read as defects")
}
