package inspect

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanCache_ReusesUnchangedDirAndRescansOnChange(t *testing.T) {
	dir := t.TempDir()
	writeFrame(t, dir, "l_1.fits", "'Light Frame'", "'L'", "120.0", 900)

	c := NewScanCache()
	inv1, err := c.ScanMany(context.Background(), []string{dir}, DefaultScanOptions())
	require.NoError(t, err)
	require.Len(t, inv1.Frames, 1)
	mt := dirMTime(dir)

	// Add a frame but restore the original mtime → an unchanged directory serves the cached scan.
	writeFrame(t, dir, "l_2.fits", "'Light Frame'", "'L'", "120.0", 920)
	require.NoError(t, os.Chtimes(dir, time.Unix(0, mt), time.Unix(0, mt)))
	inv2, err := c.ScanMany(context.Background(), []string{dir}, DefaultScanOptions())
	require.NoError(t, err)
	assert.Len(t, inv2.Frames, 1, "unchanged mtime → cached scan reused, second frame not seen")

	// Bump the mtime → the directory is re-scanned and the new frame appears.
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(dir, future, future))
	inv3, err := c.ScanMany(context.Background(), []string{dir}, DefaultScanOptions())
	require.NoError(t, err)
	assert.Len(t, inv3.Frames, 2, "changed mtime → re-scanned")
}

func TestScanCache_MergesCachedAndNewDirs(t *testing.T) {
	a := t.TempDir()
	writeFrame(t, a, "a.fits", "'Light Frame'", "'L'", "120.0", 900)
	b := t.TempDir()
	writeFrame(t, b, "b.fits", "'Light Frame'", "'L'", "120.0", 910)

	c := NewScanCache()
	inv1, err := c.ScanMany(context.Background(), []string{a}, DefaultScanOptions())
	require.NoError(t, err)
	require.Len(t, inv1.Frames, 1)

	// Adding b and re-inspecting reuses a's cached scan and only scans b — the merged result has both.
	inv2, err := c.ScanMany(context.Background(), []string{a, b}, DefaultScanOptions())
	require.NoError(t, err)
	assert.Len(t, inv2.Frames, 2)
}
