package s3store

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

// userMD5Key is the user-metadata key Upload records the file's content MD5 under (header
// "x-amz-meta-astro-md5" on the wire; minio-go canonicalizes it back to this casing on Stat). It gives
// remove-local a strong equality check even for multipart uploads, whose ETag is not a content MD5.
const userMD5Key = "Astro-Md5"

// Multipart tuning for large uploads. Above multipartThreshold, a file is split into multipartPartSize
// parts uploaded multipartThreads-at-a-time (minio ConcurrentStreamParts) so ONE big file also uses the
// full link instead of a single stream — complementary to the transfer layer's file-level parallelism,
// which already keeps many normal-sized files in flight. ConcurrentStreamParts buffers
// multipartThreads×multipartPartSize per large file in flight, so keep the part size/threads modest; the
// threshold keeps typical (≤64 MiB) captures on a single fast PUT with no buffering.
const (
	multipartThreshold = 64 << 20 // only genuinely large files use parallel multipart
	multipartPartSize  = 16 << 20 // 16 MiB parts (minio minimum is 5 MiB)
	multipartThreads   = 4        // parallel part uploads per large file
)

// Upload streams localPath to bucket/key, recording the file's content MD5 as user metadata (one extra
// disk pass — cheap next to the network transfer). onBytes (optional) is called with the delta of bytes
// sent on each read, so the caller can drive a progress bar.
func (c *Client) Upload(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error {
	sum, err := MD5File(localPath)
	if err != nil {
		return err
	}
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
	opts := minio.PutObjectOptions{
		ContentType:  contentType(localPath),
		UserMetadata: map[string]string{userMD5Key: sum},
	}
	// A large file is split into parts uploaded in parallel. ConcurrentStreamParts works on a plain stream
	// (no ReaderAt), so the countReader still ticks progress; minio reads ahead into NumThreads buffers.
	if fi.Size() > multipartThreshold {
		opts.PartSize = multipartPartSize
		opts.NumThreads = multipartThreads
		opts.ConcurrentStreamParts = true
	}
	if _, err := c.mc.PutObject(ctx, bucket, key, r, fi.Size(), opts); err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// MD5File stream-computes the hex content MD5 of a local file — what Upload records as user metadata and
// what remove-local compares against the mirror before deleting anything.
func MD5File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("md5 %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
// A transient failure re-runs the whole read.
func (c *Client) GetBytes(ctx context.Context, bucket, key string) ([]byte, error) {
	var data []byte
	err := withRetry(ctx, "get", func() error {
		obj, err := c.mc.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return err
		}
		defer func() { _ = obj.Close() }()
		data, err = io.ReadAll(obj)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	return data, nil
}

// PutBytes uploads an in-memory blob (backup manifests, appstate snapshots, …). For large files use Upload.
// Each retry attempt restarts from a fresh reader over data.
func (c *Client) PutBytes(ctx context.Context, bucket, key string, data []byte) error {
	err := withRetry(ctx, "put", func() error {
		_, perr := c.mc.PutObject(ctx, bucket, key, bytes.NewReader(data), int64(len(data)),
			minio.PutObjectOptions{ContentType: contentType(key)})
		return perr
	})
	if err != nil {
		return fmt.Errorf("s3 put %s: %w", key, err)
	}
	return nil
}

// Delete removes one object. A missing key is not an error.
func (c *Client) Delete(ctx context.Context, bucket, key string) error {
	err := withRetry(ctx, "delete", func() error {
		return c.mc.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
	})
	if err != nil {
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
