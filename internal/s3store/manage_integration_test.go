package s3store

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// mgTestClient builds a client against a MinIO endpoint from the environment, or skips. Run with:
//
//	ASTRO_TEST_S3_ENDPOINT=localhost:9100 ASTRO_TEST_S3_KEY=minioadmin ASTRO_TEST_S3_SECRET=minioadmin \
//	  go test ./internal/s3store/ -run Integration -v
func mgTestClient(t *testing.T) *Client {
	t.Helper()
	ep := os.Getenv("ASTRO_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("set ASTRO_TEST_S3_ENDPOINT (+ _KEY/_SECRET) to run S3 integration tests")
	}
	c, err := New(Config{
		Endpoint:    ep,
		Region:      "us-east-1",
		AccessKeyID: os.Getenv("ASTRO_TEST_S3_KEY"),
		SecretKey:   os.Getenv("ASTRO_TEST_S3_SECRET"),
		UseSSL:      false,
	})
	require.NoError(t, err)
	return c
}

// Exercises the object-manager operations (make/remove bucket, stream put/open, create folder, list, remove
// prefix) against a real MinIO — the same paths the UI S3 explorer drives.
func TestIntegration_ManageOps(t *testing.T) {
	c := mgTestClient(t)
	ctx := context.Background()
	const bucket = "s3store-itest"

	// Clean slate (ignore errors), then a fresh bucket.
	_ = c.RemovePrefix(ctx, bucket, "")
	_ = c.RemoveBucket(ctx, bucket)
	require.NoError(t, c.MakeBucket(ctx, bucket, "us-east-1"))
	defer func() {
		_ = c.RemovePrefix(ctx, bucket, "")
		_ = c.RemoveBucket(ctx, bucket)
	}()

	// Stream an object + create an empty folder marker.
	body := "hello-manager"
	require.NoError(t, c.PutReader(ctx, bucket, "dir/file.txt", strings.NewReader(body), int64(len(body))))
	require.NoError(t, c.CreateFolder(ctx, bucket, "empty/"))

	// Root listing shows both folders, no loose files.
	folders, files, err := c.ListDir(ctx, bucket, "")
	require.NoError(t, err)
	assert.Contains(t, folders, "dir/")
	assert.Contains(t, folders, "empty/")
	assert.Empty(t, files)

	// dir/ listing shows the file with its size.
	_, dirFiles, err := c.ListDir(ctx, bucket, "dir/")
	require.NoError(t, err)
	require.Len(t, dirFiles, 1)
	assert.Equal(t, "dir/file.txt", dirFiles[0].Key)
	assert.Equal(t, int64(len(body)), dirFiles[0].Size)

	// Open streams the bytes back with the right size.
	rc, size, err := c.Open(ctx, bucket, "dir/file.txt")
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	assert.Equal(t, body, string(got))
	assert.Equal(t, int64(len(body)), size)

	// RemovePrefix deletes the whole folder.
	require.NoError(t, c.RemovePrefix(ctx, bucket, "dir/"))
	_, after, err := c.ListDir(ctx, bucket, "dir/")
	require.NoError(t, err)
	assert.Empty(t, after)
}

// TestIntegration_ReadRange exercises the byte-range GET (the low-disk remote-scan primitive) against a
// real MinIO: a mid-object range returns exactly those bytes, and a range past EOF returns a short read.
func TestIntegration_ReadRange(t *testing.T) {
	c := mgTestClient(t)
	ctx := context.Background()
	const bucket = "s3store-itest"
	_ = c.RemovePrefix(ctx, bucket, "")
	_ = c.RemoveBucket(ctx, bucket)
	require.NoError(t, c.MakeBucket(ctx, bucket, "us-east-1"))
	defer func() { _ = c.RemovePrefix(ctx, bucket, ""); _ = c.RemoveBucket(ctx, bucket) }()

	body := []byte("0123456789abcdef")
	require.NoError(t, c.PutReader(ctx, bucket, "k", bytes.NewReader(body), int64(len(body))))

	mid, err := c.ReadRange(ctx, bucket, "k", 4, 6)
	require.NoError(t, err)
	assert.Equal(t, "456789", string(mid))

	tail, err := c.ReadRange(ctx, bucket, "k", 10, 100) // past EOF → the bytes that exist
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(tail))
}

// TestIntegration_ReadRange_FITSHeader validates the full low-disk remote-scan path: upload a real FITS
// capture, read only its header via a byte range, and classify it — without downloading the pixel data.
func TestIntegration_ReadRange_FITSHeader(t *testing.T) {
	c := mgTestClient(t)
	ctx := context.Background()
	const bucket = "s3store-itest"
	_ = c.RemovePrefix(ctx, bucket, "")
	_ = c.RemoveBucket(ctx, bucket)
	require.NoError(t, c.MakeBucket(ctx, bucket, "us-east-1"))
	defer func() { _ = c.RemovePrefix(ctx, bucket, ""); _ = c.RemoveBucket(ctx, bucket) }()

	dir := t.TempDir()
	p := fitstest.Write(t, dir, "light.fits", 64, 64, 100, map[string]string{
		"IMAGETYP": "LIGHT", "FILTER": "Ha", "GAIN": "200", "EXPTIME": "300.0",
	})
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, c.PutReader(ctx, bucket, "lum/M42/light.fits", bytes.NewReader(raw), int64(len(raw))))

	data, err := c.ReadRange(ctx, bucket, "lum/M42/light.fits", 0, 16*2880) // header blocks only
	require.NoError(t, err)
	h, _, err := fits.ReadHeaderFrom(bytes.NewReader(data))
	require.NoError(t, err)
	fr := inspect.FrameFromHeader("/data/M42/light.fits", h)
	assert.Equal(t, inspect.Light, fr.Type)
	assert.Equal(t, "Ha", fr.Filter)
	assert.EqualValues(t, 200, fr.Gain)
	assert.EqualValues(t, 300000, fr.ExposureMs)
}
