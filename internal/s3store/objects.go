package s3store

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

// Upload streams localPath to bucket/key. onBytes (optional) is called with the delta of bytes sent on
// each read, so the caller can drive a progress bar.
func (c *Client) Upload(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", localPath, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", localPath, err)
	}
	r := &countReader{r: f, onBytes: onBytes}
	_, err = c.mc.PutObject(ctx, bucket, key, r, fi.Size(), minio.PutObjectOptions{ContentType: contentType(localPath)})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Download writes bucket/key to localPath (via a temp file + atomic rename, creating parent dirs). onBytes
// (optional) reports the delta received per read. The caller is responsible for a safe localPath.
func (c *Client) Download(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error {
	obj, err := c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", localPath, err)
	}
	tmp := localPath + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(out, &countReader{r: obj, onBytes: onBytes}); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("s3 download %s: %w", key, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, localPath)
}

// GetBytes reads a whole (small) object into memory — for run.json summaries, backup manifests, appstate
// snapshots, etc. Not for large captures/results (use Download, which streams to a file with progress).
func (c *Client) GetBytes(ctx context.Context, bucket, key string) ([]byte, error) {
	obj, err := c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("s3 read %s: %w", key, err)
	}
	return data, nil
}

// PutBytes uploads an in-memory blob (backup manifests, appstate snapshots, …). For large files use Upload.
func (c *Client) PutBytes(ctx context.Context, bucket, key string, data []byte) error {
	_, err := c.mc.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType(key)})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Delete removes one object. A missing key is not an error.
func (c *Client) Delete(ctx context.Context, bucket, key string) error {
	if err := c.mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// contentType guesses a MIME type from the file extension so browsers render previews served straight
// from S3 (falls back to octet-stream). FITS has no registered type.
func contentType(path string) string {
	if ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
