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
	// DefaultStorageClass, when set, is the S3 storage class every upload writes with (empty → the
	// provider default, i.e. STANDARD). It MUST be an INSTANT class (New rejects an archived one): the
	// pipeline's own control writes — run.json, manifests, appstate — must stay immediately readable, so
	// true archival is applied AFTER the fact by the tier job, never as a write default. See glacier.go.
	DefaultStorageClass string
}

// Configured reports whether credentials are present (so callers can offer S3 features or not).
func (c Config) Configured() bool { return c.AccessKeyID != "" && c.SecretKey != "" }

// Object is one S3 object's cheap metadata. ETag is the S3 entity tag without quotes (for a single-part,
// non-SSE-C/KMS upload it equals the content MD5; a multipart ETag contains "-"). MD5 is the content MD5
// our own uploads record as user metadata (see Upload) — both feed remove-local's strong verification.
// List responses carry the ETag only; MD5 requires a Stat.
type Object struct {
	Key     string `json:"key"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time_ms"`
	ETag    string `json:"etag,omitempty"`
	MD5     string `json:"md5,omitempty"`
	// StorageClass is the object's S3 storage class ("" == STANDARD; "GLACIER"/"DEEP_ARCHIVE" == archived;
	// "GLACIER_IR" is instant despite its name). List AND Stat both carry it. See glacier.go predicates.
	StorageClass string `json:"storage_class,omitempty"`
	// Restore carries an archived object's thaw status. It is populated ONLY by Stat/HEAD — a List entry
	// never reports restore status — so readiness of an archived object always needs a Stat. nil on a List
	// entry and on any object that was never restore-requested.
	Restore *RestoreState `json:"restore,omitempty"`
}

// RestoreState is an archived object's thaw status (from the x-amz-restore header). Ongoing means a
// restore is in progress; a completed restore has Ongoing=false and ExpiryMs set to when the temporary
// readable copy lapses (0 == not restored / no expiry).
type RestoreState struct {
	Ongoing  bool  `json:"ongoing"`
	ExpiryMs int64 `json:"expiry_ms,omitempty"`
}

// Client is a reusable, concurrency-safe S3 client (minio.Client is safe for concurrent use).
type Client struct {
	mc *minio.Client
	// defaultClass is Config.DefaultStorageClass, applied to every upload (Upload/PutReader/PutBytes) when
	// non-empty. Guaranteed instant by New. A server-side move (Copy) preserves the source class instead.
	defaultClass string
}

// New builds a client from cfg. Returns ErrNoCredentials when keys are missing, or an error when
// DefaultStorageClass is an archived class (writes must stay readable — see Config.DefaultStorageClass).
func New(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, ErrNoCredentials
	}
	class := strings.ToUpper(strings.TrimSpace(cfg.DefaultStorageClass))
	if class != "" && IsArchivedClass(class) {
		return nil, fmt.Errorf("s3: default storage class %q is archived; use an instant class so writes stay readable", class)
	}
	endpoint := normalizeEndpoint(cfg.Endpoint)
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
	return &Client{mc: mc, defaultClass: class}, nil
}

// ListDir returns the immediate sub-folders (common prefixes) and files directly under prefix, using the
// "/" delimiter — the S3 analogue of os.ReadDir. Folder names include their trailing "/". A transient
// failure re-runs the whole scan (the listing channel is consumed per attempt).
func (c *Client) ListDir(ctx context.Context, bucket, prefix string) (folders []string, files []Object, err error) {
	p := prefix
	if p != "" && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	err = withRetry(ctx, "list", func() error {
		folders, files = nil, nil // a retried attempt restarts the scan from scratch
		for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: p, Recursive: false}) {
			if obj.Err != nil {
				return obj.Err
			}
			if strings.HasSuffix(obj.Key, "/") {
				folders = append(folders, obj.Key)
				continue
			}
			files = append(files, objectFrom(obj))
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("s3 list %s/%s: %w", bucket, p, err)
	}
	return folders, files, nil
}

// List returns every object under prefix (recursive) — for sync diffs and backups. A transient failure
// re-runs the whole scan (the listing channel is consumed per attempt).
func (c *Client) List(ctx context.Context, bucket, prefix string) ([]Object, error) {
	var out []Object
	err := withRetry(ctx, "list", func() error {
		out = nil // a retried attempt restarts the scan from scratch
		for obj := range c.mc.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				return obj.Err
			}
			if strings.HasSuffix(obj.Key, "/") {
				continue
			}
			out = append(out, objectFrom(obj))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("s3 list %s/%s: %w", bucket, prefix, err)
	}
	return out, nil
}

// Stat returns the object's metadata and whether it exists (a missing key is ok=false, not an error).
// Unlike a List entry, a Stat also carries the content MD5 recorded by Upload (user metadata).
func (c *Client) Stat(ctx context.Context, bucket, key string) (obj Object, ok bool, err error) {
	var info minio.ObjectInfo
	serr := withRetry(ctx, "stat", func() error {
		var e error
		info, e = c.mc.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
		return e
	})
	if serr != nil {
		if minio.ToErrorResponse(serr).StatusCode == 404 {
			return Object{}, false, nil
		}
		return Object{}, false, fmt.Errorf("s3 stat %s: %w", key, serr)
	}
	obj = Object{
		Key:          key,
		Size:         info.Size,
		ModTime:      info.LastModified.UnixMilli(),
		ETag:         strings.Trim(info.ETag, `"`),
		MD5:          userMD5(info),
		StorageClass: info.StorageClass,
	}
	// x-amz-restore, when present, tells us whether an archived object is thawing or already thawed. A zero
	// ExpiryTime (no completed restore) leaves ExpiryMs 0 rather than time.Time{}'s huge negative UnixMilli.
	if info.Restore != nil {
		rs := &RestoreState{Ongoing: info.Restore.OngoingRestore}
		if !info.Restore.ExpiryTime.IsZero() {
			rs.ExpiryMs = info.Restore.ExpiryTime.UnixMilli()
		}
		obj.Restore = rs
	}
	return obj, true, nil
}

// userMD5 extracts the content MD5 Upload records as user metadata. minio-go canonicalizes metadata keys
// to MIME-header casing ("Astro-Md5"); scan case-insensitively anyway for gateways that don't.
func userMD5(info minio.ObjectInfo) string {
	if v, ok := info.UserMetadata[userMD5Key]; ok {
		return v
	}
	for k, v := range info.UserMetadata {
		if strings.EqualFold(k, userMD5Key) {
			return v
		}
	}
	return ""
}

// BucketExists reports whether the bucket exists and is reachable (also validates credentials).
func (c *Client) BucketExists(ctx context.Context, bucket string) (bool, error) {
	var ok bool
	err := withRetry(ctx, "bucket-exists", func() error {
		var e error
		ok, e = c.mc.BucketExists(ctx, bucket)
		return e
	})
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
	return Object{Key: o.Key, Size: o.Size, ModTime: o.LastModified.UnixMilli(),
		ETag: strings.Trim(o.ETag, `"`), StorageClass: o.StorageClass}
}

// normalizeEndpoint reduces a user-entered endpoint to the bare host[:port] minio-go requires: it strips a
// scheme (http/https — the UseSSL flag, not the scheme, controls TLS) and any trailing path/query, so
// pasting a console URL like "https://s3.fr-par.scw.cloud/" works instead of erroring with
// "Endpoint url cannot have fully qualified paths".
//
// Why the heuristics exist: users paste whatever their provider's console shows — a browsable https URL,
// a bucket-scoped virtual-hosted host, or a bare region host — and the UI has a single "endpoint" field.
// The transforms below make all three spellings work without asking the user to know the difference.
//
// When to bypass: enter the exact host[:port] with no scheme and it passes through untouched — the only
// rewrite that can still fire is the virtual-hosted bucket-label strip, which skips anything that does not
// embed a ".s3." label (so "minio:9000", "s3.example.com", "storage.googleapis.com" are never rewritten).
// A provider whose real service host legitimately contains ".s3." after a non-bucket label (none known)
// would need the config to carry the host verbatim — revisit here if that ever shows up.
func normalizeEndpoint(ep string) string {
	ep = strings.TrimSpace(ep)
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.TrimPrefix(ep, "http://")
	if i := strings.IndexAny(ep, "/?"); i >= 0 {
		ep = ep[:i]
	}
	// Virtual-hosted-style endpoint (bucket baked into the host, e.g. "<bucket>.s3.fr-par.scw.cloud" or
	// "<bucket>.s3.amazonaws.com" — what a cloud console copies): drop the leading bucket label down to the
	// "s3." service host so minio addresses the bucket exactly once (path-style against the region host, or
	// virtual-host for AWS) instead of doubling it into "<bucket>/<bucket>/…", which S3 rejects with
	// NoSuchKey. A region/base endpoint ("s3.<region>.…", "s3.amazonaws.com", "minio:9000") is left as-is.
	if i := strings.Index(ep, ".s3."); i > 0 && !strings.HasPrefix(ep, "s3.") {
		ep = ep[i+1:]
	}
	return ep
}
