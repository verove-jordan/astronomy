// Package s3store is a small, reusable S3 client for AstroStack's import/sync/backup features. It wraps
// minio-go with the operations the rest of the app needs — list (folder + recursive), stat, upload and
// download (with byte progress), and delete — over a bucket the UI selects per request. Credentials come
// from the host environment ONLY (never the UI, never logged), matching the live-stacking S3 source.
//
// It deals in raw (bucket, key, localPath) terms; the local⇄S3 mirror convention (S3 key = path relative
// to the data/output dir) lives in the callers (internal/transfer, internal/backup).
package s3store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrNoCredentials is returned by New when S3 access keys are not configured.
var ErrNoCredentials = errors.New("s3: credentials not configured (set ASTRO_S3_ACCESS_KEY_ID / ASTRO_S3_SECRET_ACCESS_KEY)")

// Config is the S3 connection. Credentials originate from the host environment only.
type Config struct {
	Endpoint    string // host[:port], no scheme; empty → AWS S3
	Region      string
	AccessKeyID string
	SecretKey   string
	UseSSL      bool
}

// Configured reports whether credentials are present (so callers can offer S3 features or not).
func (c Config) Configured() bool { return c.AccessKeyID != "" && c.SecretKey != "" }

// Object is one S3 object's cheap metadata.
type Object struct {
	Key     string `json:"key"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time_ms"`
}

// Client is a reusable, concurrency-safe S3 client (minio.Client is safe for concurrent use).
type Client struct {
	mc *minio.Client
}

// New builds a client from cfg. Returns ErrNoCredentials when keys are missing.
func New(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, ErrNoCredentials
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	mc, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &Client{mc: mc}, nil
}

// ListDir returns the immediate sub-folders (common prefixes) and files directly under prefix, using the
// "/" delimiter — the S3 analogue of os.ReadDir. Folder names include their trailing "/".
func (c *Client) ListDir(ctx context.Context, bucket, prefix string) (folders []string, files []Object, err error) {
	p := prefix
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: p, Recursive: false}) {
		if obj.Err != nil {
			return nil, nil, fmt.Errorf("s3 list %s/%s: %w", bucket, p, obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			folders = append(folders, obj.Key)
			continue
		}
		files = append(files, objectFrom(obj))
	}
	return folders, files, nil
}

// List returns every object under prefix (recursive) — for sync diffs and backups.
func (c *Client) List(ctx context.Context, bucket, prefix string) ([]Object, error) {
	var out []Object
	for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list %s/%s: %w", bucket, prefix, obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		out = append(out, objectFrom(obj))
	}
	return out, nil
}

// Stat returns the object's metadata and whether it exists (a missing key is ok=false, not an error).
func (c *Client) Stat(ctx context.Context, bucket, key string) (obj Object, ok bool, err error) {
	info, serr := c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if serr != nil {
		if minio.ToErrorResponse(serr).StatusCode == 404 {
			return Object{}, false, nil
		}
		return Object{}, false, fmt.Errorf("s3 stat %s: %w", key, serr)
	}
	return Object{Key: key, Size: info.Size, ModTime: info.LastModified.UnixMilli()}, true, nil
}

// BucketExists reports whether the bucket exists and is reachable (also validates credentials).
func (c *Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	ok, err := c.mc.BucketExists(ctx, bucket)
	if err != nil {
		return false, fmt.Errorf("s3 bucket exists %s: %w", bucket, err)
	}
	return ok, nil
}

// EnsureBucket creates the bucket if it does not already exist (idempotent).
func (c *Client) EnsureBucket(ctx context.Context, bucket string) error {
	ok, err := c.mc.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("s3 bucket exists %s: %w", bucket, err)
	}
	if ok {
		return nil
	}
	if err := c.mc.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("s3 make bucket %s: %w", bucket, err)
	}
	return nil
}

// ListBuckets returns the accessible bucket names (for the UI's bucket picker).
func (c *Client) ListBuckets(ctx context.Context) ([]string, error) {
	bs, err := c.mc.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("s3 list buckets: %w", err)
	}
	names := make([]string, len(bs))
	for i, b := range bs {
		names[i] = b.Name
	}
	return names, nil
}

func objectFrom(o minio.ObjectInfo) Object {
	return Object{Key: o.Key, Size: o.Size, ModTime: o.LastModified.UnixMilli()}
}
