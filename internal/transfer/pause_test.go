package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A manual pause requested up front stops the upload before any file, with ErrPaused (not a failure).
func TestRunUpload_PausesBeforeFirstFile(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	req.PauseRequested = func() bool { return true }
	fake := newFakeS3()

	_, err := runUpload(context.Background(), fake, req, false, nil)
	require.ErrorIs(t, err, ErrPaused)
	assert.Empty(t, fake.uploadCalls, "nothing uploaded when paused up front")
}

// A pause requested after the first file uploads exactly one file, then stops with ErrPaused — proving
// the check happens BETWEEN files (a resumed sync then size-skips the uploaded one).
func TestRunUpload_PausesBetweenFiles(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	calls := 0
	req.PauseRequested = func() bool { calls++; return calls > 1 } // false, then true
	fake := newFakeS3()

	_, err := runUpload(context.Background(), fake, req, false, nil)
	require.ErrorIs(t, err, ErrPaused)
	assert.Len(t, fake.uploadCalls, 1, "one file uploaded before the pause took effect")
}

// With no pause requested the transfer completes normally (both files).
func TestRunUpload_NoPauseCompletes(t *testing.T) {
	req, _ := writeTestFolder(t)
	req.Op = OpUpload
	req.PauseRequested = func() bool { return false }
	fake := newFakeS3()

	res, err := runUpload(context.Background(), fake, req, false, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
}

// A pause during remove-local stops before deleting, with ErrPaused — the verified-safe files stay on disk.
func TestRunRemoveLocal_PausesBeforeDeleting(t *testing.T) {
	req, folder := writeTestFolder(t)
	req.Op = OpRemoveLocal
	req.PauseRequested = func() bool { return true }
	fake := newFakeS3()
	seedMirror(fake, 0) // every file strongly verified, so only the pause stops the delete

	_, err := runRemoveLocal(context.Background(), fake, req)
	require.ErrorIs(t, err, ErrPaused)
	_, statErr := os.Stat(filepath.Join(folder, "a.fits"))
	assert.NoError(t, statErr, "local file not deleted when paused")
}
