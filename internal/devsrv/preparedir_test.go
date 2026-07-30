package devsrv

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The capture folder is created by whichever process WRITES the frames — the device server, natively
// on the host. A containerized engine mounts external drives read-only, so creating it there failed
// with "read-only file system" even though the drive was perfectly writable from the host.
func TestEnsureWritableDir_CreatesNestedFolders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Pictures", "astro", "2026-07-30", "IC1590")
	require.NoError(t, ensureWritableDir(dir))

	st, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, st.IsDir())
}

func TestEnsureWritableDir_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, ensureWritableDir(dir))
	require.NoError(t, ensureWritableDir(dir), "an existing folder is not an error")
}

// The probe leaves nothing behind — a stray temp file in a capture folder would be swept up by the
// classifier as an unreadable frame.
func TestEnsureWritableDir_LeavesNoProbeFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, ensureWritableDir(dir))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "the write probe must clean up after itself")
}

// An EXISTING folder on a read-only mount passes MkdirAll and then fails on the first frame — an hour
// into the night. The probe is what catches it at Start instead.
func TestEnsureWritableDir_RejectsAnUnwritableExistingFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not restrain this user")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	require.NoError(t, os.Mkdir(dir, 0o500)) // readable and listable, not writable
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := ensureWritableDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not writable",
		"the message must say it cannot be written to, not merely that mkdir failed")
}
