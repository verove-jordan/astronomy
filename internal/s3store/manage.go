package s3store

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
)

// Management operations for the UI S3 explorer (create/delete buckets & folders, stream objects). These are
// intentionally not used by the pipeline paths, which deal in whole-folder transfers.

// MakeBucket creates a bucket (errors if it already exists — the caller decides whether that is fine).
func (c *Client) MakeBucket(ctx context.Context, bucket, region string) error {
	if err := c.mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		return fmt.Errorf("s3 make bucket %s: %w", bucket, err)
	}
	return nil
}

// RemoveBucket deletes an empty bucket (minio refuses a non-empty one; the handler empties it first when the
// user asks to force-delete).
func (c *Client) RemoveBucket(ctx context.Context, bucket string) error {
	if err := c.mc.RemoveBucket(ctx, bucket); err != nil {
		return fmt.Errorf("s3 remove bucket %s: %w", bucket, err)
	}
	return nil
}

// CreateFolder writes a zero-byte marker object at key (S3 has no real folders) so an empty folder shows in
// the browser. key is normalized to end with "/".
func (c *Client) CreateFolder(ctx context.Context, bucket, key string) error {
	if !strings.HasSuffix(key, "/") {
		key += "/"
	}
	return c.PutBytes(ctx, bucket, key, []byte{})
}

// RemovePrefix deletes every object under prefix (recursively, including folder markers) — the explorer's
// "delete folder". A recursive list feeds minio's bulk RemoveObjects.
//
// NOTE(scale): this streams one listing into one RemoveObjects call — fine for explorer-sized folders
// (thousands of objects), but a prefix with millions of objects would hold a single slow request open
// with no progress reporting or partial-failure resume. If that ever becomes a real use, batch the
// listing into pages and surface progress like the transfer jobs do. Also note list errors are skipped
// (continue) so a truncated listing deletes what it saw — acceptable for "empty this folder" semantics.
func (c *Client) RemovePrefix(ctx context.Context, bucket, prefix string) error {
	objectsCh := make(chan minio.ObjectInfo)
	go func() {
		defer close(objectsCh)
		for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				continue
			}
			select {
			case objectsCh <- obj:
			case <-ctx.Done():
				return
			}
		}
	}()
	for e := range c.mc.RemoveObjects(ctx, bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		if e.Err != nil {
			return fmt.Errorf("s3 remove %s: %w", e.ObjectName, e.Err)
		}
	}
	return nil
}

// Open streams an object for download, returning its size for the Content-Length. The caller closes the
// reader.
func (c *Client) Open(ctx context.Context, bucket, key string) (io.ReadCloser, int64, error) {
	obj, err := c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("s3 get %s: %w", key, err)
	}
	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()
		return nil, 0, fmt.Errorf("s3 stat %s: %w", key, err)
	}
	return obj, info.Size, nil
}

// PutReader streams r to bucket/key. size may be -1 when unknown (minio buffers into a multipart upload).
func (c *Client) PutReader(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	if _, err := c.mc.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType(key)}); err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}
