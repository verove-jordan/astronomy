package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// frameExts are the capture-file extensions an S3 listing is filtered to (FITS + one-shot-color
// stills), mirroring what inspect discovers on a local disk. Object keys with any other extension
// (logs, sidecars, thumbnails) are ignored.
var frameExts = map[string]bool{
	".fits": true, ".fit": true, ".fts": true,
	".dng": true, ".heic": true, ".heif": true,
	".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true,
}

// S3Config configures an S3Source. Credentials originate from the host environment (never the UI);
// Bucket/Prefix come from the job request; DownloadDir is the local mirror the engine reads.
type S3Config struct {
	Endpoint    string // host[:port], no scheme; empty → AWS S3
	Region      string
	AccessKeyID string
	SecretKey   string
	UseSSL      bool
	Bucket      string
	Prefix      string
	DownloadDir string
}

// S3Source lists objects under a bucket/prefix and mirrors each into DownloadDir so the engine can
// read ordinary local files. S3 objects are written atomically, so no write-stability gate is needed.
type S3Source struct {
	client      *minio.Client
	bucket      string
	prefix      string
	downloadDir string
}

// NewS3 builds an S3Source. It validates the minimum configuration and creates the local mirror dir.
func NewS3(cfg S3Config) (*S3Source, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 source: bucket is required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3 source: credentials missing (set ASTRO_S3_ACCESS_KEY_ID / ASTRO_S3_SECRET_ACCESS_KEY)")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 source: new client: %w", err)
	}
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		return nil, fmt.Errorf("s3 source: create download dir: %w", err)
	}
	return &S3Source{client: client, bucket: cfg.Bucket, prefix: cfg.Prefix, downloadDir: cfg.DownloadDir}, nil
}

// List enumerates frame objects under the prefix. Non-frame keys and "directory" placeholder keys
// are skipped.
func (s *S3Source) List(ctx context.Context) ([]Object, error) {
	var out []Object
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: s.prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list %s/%s: %w", s.bucket, s.prefix, obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		if !frameExts[strings.ToLower(filepath.Ext(obj.Key))] {
			continue
		}
		out = append(out, Object{Key: obj.Key, Size: obj.Size, ModTime: obj.LastModified.UnixMilli()})
	}
	return out, nil
}

// Fetch downloads o into the local mirror (preserving the key's sub-path so bucket "folders" like
// lights/ and darks/ survive — the inspector also classifies by folder). An already-present file of
// the same size is reused, so a re-list never re-downloads.
func (s *S3Source) Fetch(ctx context.Context, o Object) (string, error) {
	rel := strings.TrimPrefix(strings.TrimPrefix(o.Key, s.prefix), "/")
	local := filepath.Join(s.downloadDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(local, filepath.Clean(s.downloadDir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("s3 fetch: key %q escapes download dir", o.Key)
	}
	if info, err := os.Stat(local); err == nil && info.Size() == o.Size {
		return local, nil
	}
	if err := s.client.FGetObject(ctx, s.bucket, o.Key, local, minio.GetObjectOptions{}); err != nil {
		return "", fmt.Errorf("s3 get %s: %w", o.Key, err)
	}
	return local, nil
}

// LocalRoot is the local mirror directory the pipeline inspects and finalizes over.
func (s *S3Source) LocalRoot() string { return s.downloadDir }

// Close releases nothing (the minio client holds no long-lived connection to close).
func (s *S3Source) Close() error { return nil }
