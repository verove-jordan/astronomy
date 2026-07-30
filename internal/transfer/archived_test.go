package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// A download whose pull set contains an archived-and-not-restored object must fail fast with an
// *ArchivedError naming the cold keys (so the job can thaw them), NOT stream a doomed GetObject. Once the
// restore completes, the same pull succeeds. A List-confirmed instant object is never a blocker.
func TestDownloadArchivedPreflight(t *testing.T) {
	f := newFakeS3()
	// A cold (Glacier, no restore) object and a hot (Standard) one under the same folder.
	f.objects["acct/data/M101/cold.fits"] = s3store.Object{Key: "acct/data/M101/cold.fits", Size: 5, StorageClass: "GLACIER"}
	f.objects["acct/data/M101/hot.fits"] = s3store.Object{Key: "acct/data/M101/hot.fits", Size: 3, StorageClass: "STANDARD"}

	root := t.TempDir()
	req := Request{Op: OpDownload, LocalRoot: root, RelPath: "M101", Bucket: "b", KeyPrefix: "acct/data"}

	_, err := runDownload(context.Background(), f, req, nil)
	var archErr *ArchivedError
	require.True(t, errors.As(err, &archErr), "got %v", err)
	assert.Equal(t, []string{"acct/data/M101/cold.fits"}, archErr.Keys, "only the cold object blocks")
	// Nothing was downloaded — the pre-flight returns before streaming.
	assert.Equal(t, 0, f.downloadCalls["acct/data/M101/cold.fits"])
	assert.Equal(t, 0, f.downloadCalls["acct/data/M101/hot.fits"])

	// Restore completes → the cold object is readable → the same pull now succeeds and writes both files.
	obj := f.objects["acct/data/M101/cold.fits"]
	obj.Restore = &s3store.RestoreState{Ongoing: false, ExpiryMs: 1 << 40}
	f.objects["acct/data/M101/cold.fits"] = obj

	res, err := runDownload(context.Background(), f, req, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Files)
	_, statErr := os.Stat(filepath.Join(root, "M101", "cold.fits"))
	assert.NoError(t, statErr)
}

// An all-instant folder never triggers the archived path (and, since nothing is ever archived on an
// endpoint without Glacier, this is also the natural soft-fail there).
func TestDownloadAllInstantNoArchivedError(t *testing.T) {
	f := newFakeS3()
	f.objects["acct/data/M101/a.fits"] = s3store.Object{Key: "acct/data/M101/a.fits", Size: 4, StorageClass: "STANDARD"}
	root := t.TempDir()
	req := Request{Op: OpDownload, LocalRoot: root, RelPath: "M101", Bucket: "b", KeyPrefix: "acct/data"}
	res, err := runDownload(context.Background(), f, req, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Files)
}
