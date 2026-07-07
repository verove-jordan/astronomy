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
