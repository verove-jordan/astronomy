package s3store

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_ChangeStorageClass validates the subtlest correctness point of the Glacier support: a
// class change is a CopyObject onto the SAME key with ReplaceMetadata, and it MUST carry the object's user
// metadata (the Astro-Md5 remove-local/verify rely on) and content-type forward. MinIO ignores the actual
// storage class, but the metadata-preservation is exactly what a self-copy could silently drop — so this
// exercises the real code path and asserts the Astro-Md5 survives. Skips unless a MinIO endpoint is set.
func TestIntegration_ChangeStorageClass(t *testing.T) {
	c := mgTestClient(t)
	ctx := context.Background()
	const bucket = "s3store-itest"
	_ = c.RemovePrefix(ctx, bucket, "")
	_ = c.RemoveBucket(ctx, bucket)
	require.NoError(t, c.MakeBucket(ctx, bucket, "us-east-1"))
	defer func() { _ = c.RemovePrefix(ctx, bucket, ""); _ = c.RemoveBucket(ctx, bucket) }()

	// Upload via the real Upload path so the object carries an Astro-Md5 user-metadata (like every capture).
	dir := t.TempDir()
	local := dir + "/frame.fits"
	require.NoError(t, os.WriteFile(local, []byte("some-frame-bytes"), 0o644))
	require.NoError(t, c.Upload(ctx, bucket, "M42/frame.fits", local, nil))

	before, ok, err := c.Stat(ctx, bucket, "M42/frame.fits")
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, before.MD5, "upload recorded an Astro-Md5")

	// Change the class (to an instant one MinIO accepts) and confirm the Md5 metadata survived the self-copy.
	require.NoError(t, c.ChangeStorageClass(ctx, bucket, "M42/frame.fits", ClassStandard))

	after, ok, err := c.Stat(ctx, bucket, "M42/frame.fits")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, before.MD5, after.MD5, "Astro-Md5 user metadata is preserved across a class change")
	assert.Equal(t, before.Size, after.Size, "size is unchanged")

	// And the bytes are still readable/intact.
	rc, _, err := c.Open(ctx, bucket, "M42/frame.fits")
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(rc)
	assert.Equal(t, "some-frame-bytes", strings.TrimRight(buf.String(), "\x00"))
}
