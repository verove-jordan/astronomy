package s3store

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
