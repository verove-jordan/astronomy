package transfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// testClient builds an s3store client against a MinIO endpoint from the environment, or skips. Run with:
//
//	ASTRO_TEST_S3_ENDPOINT=localhost:9100 ASTRO_TEST_S3_KEY=minioadmin ASTRO_TEST_S3_SECRET=minioadmin \
//	  go test ./internal/transfer/ -run Integration -v
func testClient(t *testing.T) *s3store.Client {
	t.Helper()
	ep := os.Getenv("ASTRO_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("set ASTRO_TEST_S3_ENDPOINT (+ _KEY/_SECRET) to run S3 integration tests")
	}
	c, err := s3store.New(s3store.Config{
		Endpoint:    ep,
		Region:      "us-east-1",
		AccessKeyID: os.Getenv("ASTRO_TEST_S3_KEY"),
		SecretKey:   os.Getenv("ASTRO_TEST_S3_SECRET"),
		UseSSL:      false,
	})
	require.NoError(t, err)
	return c
}

func TestIntegration_UploadSyncDownloadRemove(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	const bucket = "astro-test"
	require.NoError(t, client.EnsureBucket(ctx, bucket))

	// A local capture folder with a nested light.
	root := t.TempDir()
	folder := filepath.Join(root, "M101")
	require.NoError(t, os.MkdirAll(filepath.Join(folder, "lights"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "a.fits"), []byte("aaaaa"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "lights", "b.fits"), []byte("bbb"), 0o644))

	// unique key prefix per run so repeated runs don't collide
	prefix := "itest/" + t.Name()
	req := Request{LocalRoot: root, RelPath: "M101", Bucket: bucket, KeyPrefix: prefix}

	// 1) Upload → 2 files, 8 bytes.
	up, err := Run(ctx, client, with(req, OpUpload), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, up.Files)
	assert.Equal(t, int64(8), up.Bytes)

	// 2) Sync again → nothing to do (all present, same size).
	sy, err := Run(ctx, client, with(req, OpSync), nil)
	require.NoError(t, err)
	assert.Equal(t, 0, sy.Files)
	assert.Equal(t, 2, sy.Skipped)

	// 3) Corrupt the mirror: overwrite one object with SAME-SIZE different bytes and no MD5 metadata
	// (PutBytes doesn't record it). Size-only verification would be fooled; the strong check (single-part
	// ETag == content MD5 here) must refuse and delete nothing.
	require.NoError(t, client.PutBytes(ctx, bucket, prefix+"/M101/a.fits", []byte("XXXXX")))
	_, err = Run(ctx, client, with(req, OpRemoveLocal), nil)
	require.Error(t, err, "remove-local must refuse a same-size different-content mirror")
	assert.FileExists(t, filepath.Join(folder, "a.fits"))
	assert.FileExists(t, filepath.Join(folder, "lights", "b.fits"), "abort-all: nothing deleted")

	// 4) Re-upload properly (records MD5 metadata) → remove-local verifies strongly and deletes.
	_, err = Run(ctx, client, with(req, OpUpload), nil)
	require.NoError(t, err)
	rm, err := Run(ctx, client, with(req, OpRemoveLocal), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, rm.Files)
	assert.Empty(t, rm.Warnings, "fresh uploads carry MD5 metadata — no size-only fallback")
	assert.NoFileExists(t, filepath.Join(folder, "a.fits"))
	assert.NoFileExists(t, filepath.Join(folder, "lights", "b.fits"))

	// 5) Download → files come back with identical content.
	dn, err := Run(ctx, client, with(req, OpDownload), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, dn.Files)
	got, err := os.ReadFile(filepath.Join(folder, "a.fits"))
	require.NoError(t, err)
	assert.Equal(t, "aaaaa", string(got))

	// cleanup S3
	objs, _ := client.List(ctx, bucket, prefix)
	for _, o := range objs {
		_ = client.Delete(ctx, bucket, o.Key)
	}
}

func TestIntegration_RemoveLocalAbortsWhenNotBackedUp(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	const bucket = "astro-test"
	require.NoError(t, client.EnsureBucket(ctx, bucket))

	root := t.TempDir()
	folder := filepath.Join(root, "M42")
	require.NoError(t, os.MkdirAll(folder, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "a.fits"), []byte("aaaaa"), 0o644))

	req := Request{LocalRoot: root, RelPath: "M42", Bucket: bucket, KeyPrefix: "itest/" + t.Name()}
	// Not uploaded → remove-local must refuse and delete nothing.
	_, err := Run(ctx, client, with(req, OpRemoveLocal), nil)
	require.Error(t, err)
	assert.FileExists(t, filepath.Join(folder, "a.fits"), "nothing is deleted when not verified on S3")
}

func with(r Request, op Op) Request { r.Op = op; return r }
