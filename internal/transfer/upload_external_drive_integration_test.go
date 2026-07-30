package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/s3conn"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/secret"
	"github.com/verove-jordan/astronomy/internal/store"
)

// TestIntegration_UploadExternalDriveToDashboardS3 recursively copies an external-drive folder (default
// /Volumes/Elements/Pictures/astro/09_05_2026) to the S3 connection configured in the dashboard, mirroring
// the tree well-sorted under "<prefix>/<folderName>/…". It is the "inspect an external drive and copy it to
// S3" flow, run headless as a test.
//
// It is a SMART sync, not a blind push: it scans the mirror first and uploads only what is MISSING or
// CORRUPTED. "Corrupted" is real content verification — Op=OpSync with Verify=true re-checks every
// same-size object against its stored content MD5 (verifyMirrored), so a half-written or bit-rotted mirror
// object is re-uploaded instead of trusted. Re-running it is cheap: already-good files are skipped.
//
// Credentials come from the dashboard's DEFAULT S3 connection (decrypted from Postgres), exactly as the app
// resolves them (Server.s3Config / Manager.s3ConfigResolved) — falling back to ASTRO_S3_* env creds when no
// DB / no default connection is present. Secrets are never hardcoded here.
//
// It is OPT-IN and never runs during `go test ./...`: it skips unless ASTRO_UPLOAD_RUN is truthy, and skips
// cleanly when the drive is unplugged or no S3 is configured. A multi-GB copy far exceeds the default
// 10-minute test timeout, so run with -timeout 0:
//
//	ASTRO_UPLOAD_RUN=1 go test ./internal/transfer/ \
//	  -run TestIntegration_UploadExternalDriveToDashboardS3 -v -timeout 0
//
// Optional overrides: ASTRO_UPLOAD_SRC (source folder), ASTRO_UPLOAD_BUCKET / ASTRO_S3_BUCKET (target
// bucket; auto-detected when the connection has exactly one), ASTRO_UPLOAD_PREFIX (S3 key prefix, default
// "astro"), ASTRO_UPLOAD_RERUN=1 (after the copy, assert a second verified sync uploads nothing — doubles
// the disk read of a large folder, so it is off by default).
//
// NOTE: dotfiles, dot-directories and *.part files are skipped (walkLocalFiles), matching the app's normal
// upload behavior — this copies the whole visible tree, not OS-hidden junk.
func TestIntegration_UploadExternalDriveToDashboardS3(t *testing.T) {
	if !truthyEnv("ASTRO_UPLOAD_RUN") {
		t.Skip("set ASTRO_UPLOAD_RUN=1 to copy an external-drive folder to the dashboard S3 connection")
	}
	ctx := context.Background()

	// The source lives on an external drive — skip (like the sibling upload test) when it is unplugged.
	src := envOr("ASTRO_UPLOAD_SRC", "/Volumes/Elements/Pictures/astro/09_05_2026")
	info, err := os.Stat(src)
	if err != nil {
		t.Skipf("source directory %q not available (drive unplugged?): %v", src, err)
	}
	require.Truef(t, info.IsDir(), "source %q is not a directory", src)

	// Resolve the SAME S3 config the pipeline uses: dashboard default connection → ASTRO_S3_* env.
	cfg := resolveDashboardS3(ctx, t)
	if !cfg.Configured() {
		t.Skip("no S3 configured — connect one in Processing → Storage (or set ASTRO_S3_*), then re-run")
	}
	client, err := s3store.New(cfg)
	require.NoError(t, err)

	bucket := pickBucket(ctx, t, client)

	// Fail fast (before walking the tree) if the bucket is unreachable or the credentials are wrong.
	exists, err := client.BucketExists(ctx, bucket)
	require.NoError(t, err)
	require.Truef(t, exists, "bucket %q not found or not accessible with the dashboard credentials", bucket)

	// Mirror the folder under "<prefix>/<folderName>/…" — structure preserved, tidily namespaced.
	req := Request{
		Op:        OpSync,
		Verify:    true, // upload only what is missing OR corrupted
		LocalRoot: filepath.Dir(src),
		RelPath:   filepath.Base(src),
		Bucket:    bucket,
		KeyPrefix: envOr("ASTRO_UPLOAD_PREFIX", "astro"),
	}
	t.Logf("smart-sync %q → s3://%s/%s (verify on: uploads only missing/corrupted files)",
		src, bucket, req.baseKey())

	res, err := Run(ctx, client, req, progressBar())
	fmt.Fprintln(os.Stderr) // end the in-place progress line
	require.NoError(t, err)
	t.Logf("done: uploaded %d files (%s), skipped %d already-good files",
		res.Files, humanBytes(res.Bytes), res.Skipped)

	// End-to-end: every visible local file is now present on the mirror under the prefix.
	local, _, err := walkLocalFiles(req.folderDir(), nil, false)
	require.NoError(t, err)
	require.NotEmptyf(t, local, "no files found under %q", src)
	objs, err := client.List(ctx, bucket, req.baseKey())
	require.NoError(t, err)
	assert.GreaterOrEqualf(t, len(objs), len(local),
		"listed %d objects under %q but the folder has %d files", len(objs), req.baseKey(), len(local))

	// Optional idempotency proof: a second verified sync must upload nothing (opt-in — it re-reads and
	// MD5s every file, doubling the disk I/O of a large capture).
	if truthyEnv("ASTRO_UPLOAD_RERUN") {
		res2, err := Run(ctx, client, req, progressBar())
		fmt.Fprintln(os.Stderr)
		require.NoError(t, err)
		assert.Equalf(t, 0, res2.Files, "a re-run of a verified sync must re-upload nothing (got %d)", res2.Files)
		assert.Equalf(t, len(local), res2.Skipped, "a re-run must recognise all %d files as mirrored", len(local))
	}
}

// resolveDashboardS3 returns the S3 config the app itself would use: the dashboard's DEFAULT connection
// (decrypted from Postgres), falling back to ASTRO_S3_* env credentials whenever the DB, the encryption key,
// or a default connection is unavailable — mirroring Server.s3Config / Manager.s3ConfigResolved so a test
// upload lands on the very same bucket the pipeline reads/writes.
func resolveDashboardS3(ctx context.Context, t *testing.T) s3store.Config {
	t.Helper()
	cfg := config.Load()
	envCfg := s3store.Config{
		Endpoint:    cfg.S3Endpoint,
		Region:      cfg.S3Region,
		AccessKeyID: cfg.S3AccessKeyID,
		SecretKey:   cfg.S3SecretAccessKey,
		UseSSL:      cfg.S3UseSSL,
	}
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Logf("Postgres unavailable (%v) — using ASTRO_S3_* env credentials", err)
		return envCfg
	}
	t.Cleanup(st.Close)
	box, err := secret.NewBox(cfg.EncryptionKey, cfg.SecretKeyFile)
	if err != nil {
		t.Logf("S3-connection encryption unavailable (%v) — using ASTRO_S3_* env credentials", err)
		return envCfg
	}
	dbCfg, ok, err := s3conn.New(st, box).DefaultConfig(ctx)
	switch {
	case err != nil:
		t.Logf("resolve default S3 connection: %v — using ASTRO_S3_* env credentials", err)
		return envCfg
	case !ok:
		t.Log("no default S3 connection set in the dashboard — using ASTRO_S3_* env credentials")
		return envCfg
	default:
		t.Logf("using dashboard default S3 connection (endpoint %s, region %s)", dbCfg.Endpoint, dbCfg.Region)
		return dbCfg
	}
}

// pickBucket resolves the target bucket: ASTRO_UPLOAD_BUCKET / ASTRO_S3_BUCKET when set, else the sole
// bucket on the connection (auto-detected), else a fatal asking the user to choose one.
func pickBucket(ctx context.Context, t *testing.T, client *s3store.Client) string {
	t.Helper()
	if b := envOr("ASTRO_UPLOAD_BUCKET", os.Getenv("ASTRO_S3_BUCKET")); b != "" {
		return b
	}
	buckets, err := client.ListBuckets(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, buckets, "the S3 connection exposes no buckets — set ASTRO_UPLOAD_BUCKET")
	if len(buckets) == 1 {
		return buckets[0]
	}
	t.Fatalf("multiple buckets on this connection %v — set ASTRO_UPLOAD_BUCKET to choose one", buckets)
	return ""
}

// progressBar returns a transfer progress callback that renders a single, in-place updating bar to stderr
// (redrawn on each whole-percent step, file boundary, or completion — the transfer already throttles the
// underlying callbacks to ~1 MiB). Print a newline after Run to finish the line.
func progressBar() func(Progress) {
	const width = 34
	start := time.Now()
	lastPct, lastFiles := -1, -1
	return func(p Progress) {
		pct := 100
		if p.BytesTotal > 0 {
			pct = int(p.BytesDone * 100 / p.BytesTotal)
		}
		done := p.BytesTotal > 0 && p.BytesDone >= p.BytesTotal
		if pct == lastPct && p.Files == lastFiles && !done {
			return
		}
		lastPct, lastFiles = pct, p.Files
		filled := pct * width / 100
		if filled > width {
			filled = width
		}
		bar := strings.Repeat("=", filled) + strings.Repeat(" ", width-filled)
		fmt.Fprintf(os.Stderr, "\r  [%s] %3d%%  %d/%d files  %s / %s  %s ",
			bar, pct, p.Files, p.TotalFiles,
			humanBytes(p.BytesDone), humanBytes(p.BytesTotal), time.Since(start).Truncate(time.Second))
	}
}

// humanBytes formats a byte count as a binary-unit string (e.g. "3.4 GiB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
