package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// TestIntegration_UploadDetecteevToScaleway uploads the ENTIRE local directory (default
// /Volumes/Elements/detecteev) to the Scaleway "astrophoto" bucket, mirroring the tree under the key
// prefix "detecteev/". It reuses Run(OpUpload), which walks the folder recursively, sets content-type,
// and lets minio-go handle multipart for large files — no bespoke upload logic.
//
// The Scaleway virtual-hosted URL "astrophoto.s3.fr-par.scw.cloud" is split into its parts: endpoint
// "s3.fr-par.scw.cloud" + bucket "astrophoto" + region "fr-par"; minio-go reconstructs that host itself.
//
// It is OPT-IN and never runs during `go test ./...`: it skips unless ASTRO_UPLOAD_RUN is truthy.
// Credentials come from the environment ONLY (never hardcoded). A multi-GB upload far exceeds the
// default 10-minute test timeout, so run with -timeout 0:
//
//	ASTRO_UPLOAD_RUN=1 \
//	ASTRO_S3_ACCESS_KEY_ID=... ASTRO_S3_SECRET_ACCESS_KEY=... \
//	go test ./internal/transfer/ -run TestIntegration_UploadDetecteevToScaleway -v -timeout 0
//
// Optional overrides: ASTRO_UPLOAD_SRC, ASTRO_S3_ENDPOINT (default s3.fr-par.scw.cloud),
// ASTRO_S3_REGION (default fr-par), ASTRO_S3_BUCKET (default astrophoto), ASTRO_S3_USE_SSL
// (default true), ASTRO_UPLOAD_PREFIX (extra S3 key prefix, default none).
//
// NOTE: dotfiles, dot-directories and *.part files are skipped (walkLocalFiles), matching the app's
// normal upload behavior — this uploads the whole visible tree, not OS-hidden junk.
func TestIntegration_UploadAstroToScaleway(t *testing.T) {

	accessKey := "SCW45R1RT39EETCNM955"                 //os.Getenv("ASTRO_S3_ACCESS_KEY_ID")
	secretKey := "516eb6e9-cfc9-44dd-b4cf-1027ab147f0d" //os.Getenv("ASTRO_S3_SECRET_ACCESS_KEY")
	if accessKey == "" || secretKey == "" {
		t.Fatal("ASTRO_UPLOAD_RUN is set but ASTRO_S3_ACCESS_KEY_ID / ASTRO_S3_SECRET_ACCESS_KEY are missing")
	}

	src := envOr("ASTRO_UPLOAD_SRC", "/Volumes/Elements/Pictures/astro")
	info, err := os.Stat(src)
	require.NoErrorf(t, err, "source directory %q must exist and be readable", src)
	require.Truef(t, info.IsDir(), "source %q is not a directory", src)

	bucket := envOr("ASTRO_S3_BUCKET", "astrophoto")
	client, err := s3store.New(s3store.Config{
		Endpoint:    envOr("ASTRO_S3_ENDPOINT", "s3.fr-par.scw.cloud"),
		Region:      envOr("ASTRO_S3_REGION", "fr-par"),
		AccessKeyID: accessKey,
		SecretKey:   secretKey,
		UseSSL:      boolEnv("ASTRO_S3_USE_SSL", true),
	})
	require.NoError(t, err)

	ctx := context.Background()

	// Fail early (before walking the tree) if the credentials are wrong or the bucket is unreachable.
	// We do not create the bucket — "astrophoto" is expected to already exist.
	exists, err := client.BucketExists(ctx, bucket)
	require.NoError(t, err)
	require.Truef(t, exists, "bucket %q not found or not accessible with these credentials", bucket)

	// Upload the whole folder. Keys become "<ASTRO_UPLOAD_PREFIX>/detecteev/<path-in-folder>".
	req := Request{
		Op:        OpUpload,
		LocalRoot: filepath.Dir(src),  // e.g. /Volumes/Elements
		RelPath:   filepath.Base(src), // e.g. detecteev
		Bucket:    bucket,
		KeyPrefix: os.Getenv("ASTRO_UPLOAD_PREFIX"),
	}

	res, err := Run(ctx, client, req, uploadProgress())
	require.NoError(t, err)
	require.Greaterf(t, res.Files, 0, "no files uploaded from %q", src)
	t.Logf("uploaded %d files (%d bytes) to s3://%s/%s", res.Files, res.Bytes, bucket, req.baseKey())

	// Verify end-to-end: every uploaded object is now listable under the prefix on S3.
	objs, err := client.List(ctx, bucket, req.baseKey())
	require.NoError(t, err)
	assert.GreaterOrEqualf(t, len(objs), res.Files,
		"listed %d objects under %q but uploaded %d", len(objs), req.baseKey(), res.Files)
}

// uploadProgress returns a throttled progress callback that streams milestones to stderr (visible
// live during the long upload, unlike buffered t.Log) roughly every 64 MiB and on the final file.
func uploadProgress() func(Progress) {
	const step = int64(64 << 20) // 64 MiB
	next := step
	return func(p Progress) {
		if p.BytesDone < next && p.Files < p.TotalFiles {
			return
		}
		for p.BytesDone >= next {
			next += step
		}
		fmt.Fprintf(os.Stderr, "  [upload] %d/%d files  %.0f/%.0f MiB\n",
			p.Files, p.TotalFiles, float64(p.BytesDone)/(1<<20), float64(p.BytesTotal)/(1<<20))
	}
}

// envOr returns the environment value for key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// truthyEnv reports whether key is set to a truthy value ("1", "true", ...).
func truthyEnv(key string) bool {
	b, _ := strconv.ParseBool(os.Getenv(key))
	return b
}

// boolEnv parses key as a bool, falling back to def when unset or unparseable.
func boolEnv(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
