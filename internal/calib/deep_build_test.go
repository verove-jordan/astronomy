package calib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDropMissing_FiltersGhostPaths guards the deep-pool hygiene: catalogued frames whose file was
// freed (e.g. after an S3 mirror) are dropped with a counted warning instead of becoming dangling
// symlinks that sink the whole Siril stack ("Opening image N failed").
func TestDropMissing_FiltersGhostPaths(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "a.fits")
	require.NoError(t, os.WriteFile(real, []byte("x"), 0o644))

	got, warn := dropMissing([]string{real, filepath.Join(dir, "ghost.fits")}, "bias pool")
	assert.Equal(t, []string{real}, got)
	assert.Contains(t, warn, "bias pool: skipped 1 frame(s)")

	got, warn = dropMissing([]string{real}, "bias pool")
	assert.Equal(t, []string{real}, got)
	assert.Empty(t, warn, "no ghosts → no warning")
}

// TestDropNonFITS_FiltersProcessedImages guards the second pool-hygiene rule: a non-FITS row that
// slipped into the catalog (a processed TIFF once misclassified as calibration) must be excluded
// with a counted warning — linked into a Siril sequence it fails the whole master with a bare
// "link: generic error", which is exactly how the phantom master_BIAS_g0o0_b0 sank a real run.
func TestDropNonFITS_FiltersProcessedImages(t *testing.T) {
	got, warn := dropNonFITS([]string{"/d/a.fits", "/d/m27_R_stacked.tif", "/d/b.FIT", "/d/c.heic"}, "bias pool", "prior warn")
	assert.Equal(t, []string{"/d/a.fits", "/d/b.FIT"}, got)
	assert.Contains(t, warn, "prior warn")
	assert.Contains(t, warn, "bias pool: skipped 2 non-FITS file(s)")

	got, warn = dropNonFITS([]string{"/d/a.fits"}, "dark pool", "")
	assert.Equal(t, []string{"/d/a.fits"}, got)
	assert.Empty(t, warn)
}
