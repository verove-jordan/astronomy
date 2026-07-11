package s3store

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// ReadRange reads up to n bytes starting at offset off from bucket/key into memory (a partial GET via the
// S3 Range header). It backs the low-disk remote scan, which reads only a FITS file's primary header (the
// first few KB) instead of downloading the whole multi-MB capture. A transient failure re-runs the whole
// read. A short object returns the bytes that exist in the range (not an error); the caller's header
// parser fails cleanly if what it got is incomplete.
func (c *Client) ReadRange(ctx context.Context, bucket, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	var data []byte
	err := withRetry(ctx, "get-range", func() error {
		opts := minio.GetObjectOptions{}
		if rerr := opts.SetRange(off, off+n-1); rerr != nil { // SetRange end is inclusive
			return rerr
		}
		obj, gerr := c.mc.GetObject(ctx, bucket, key, opts)
		if gerr != nil {
			return gerr
		}
		defer func() { _ = obj.Close() }()
		var rerr error
		data, rerr = io.ReadAll(obj)
		return rerr
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get-range %s [%d,%d): %w", key, off, off+n, err)
	}
	return data, nil
}
