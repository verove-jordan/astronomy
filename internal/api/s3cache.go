package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// s3Cache speeds up S3 browsing two independent ways:
//
//   - client reuse: it caches one minio client per connection so folder navigation reuses a warm
//     HTTP keep-alive connection instead of paying a cold TLS handshake on every request (the
//     dominant cost — ~1.8s per browse without it);
//   - listing memoization: it caches a directory's immediate folders/files for a few seconds so the
//     Miller-column ancestor fan-out and back-and-forth navigation don't re-hit S3.
//
// Both are keyed by a hash of the *resolved* connection config, so a rotated or edited connection
// transparently gets a fresh client and a cold cache — no manual invalidation needed.
type s3Cache struct {
	clients sync.Map // fingerprint -> *s3store.Client

	mu   sync.Mutex
	list map[string]s3ListEntry // fingerprint|bucket|prefix -> listing
	ttl  time.Duration
}

type s3ListEntry struct {
	folders []string
	files   []s3store.Object
	exp     time.Time
}

func newS3Cache() *s3Cache {
	return &s3Cache{list: map[string]s3ListEntry{}, ttl: 8 * time.Second}
}

// s3Client returns a keep-alive-reusing client for cfg. Nil-safe: a Server built without a cache
// (e.g. in a focused test) falls back to a one-off client.
func (s *Server) s3Client(cfg s3store.Config) (*s3store.Client, error) {
	if s.s3cache == nil {
		return s3store.New(cfg)
	}
	return s.s3cache.client(cfg)
}

// listDirCached lists a directory's immediate folders/files through the short-TTL cache (bypassed
// when fresh). Nil-safe like s3Client.
func (s *Server) listDirCached(ctx context.Context, cfg s3store.Config, bucket, prefix string, fresh bool) ([]string, []s3store.Object, error) {
	if s.s3cache == nil {
		cl, err := s3store.New(cfg)
		if err != nil {
			return nil, nil, err
		}
		return cl.ListDir(ctx, bucket, prefix)
	}
	return s.s3cache.listDir(ctx, cfg, bucket, prefix, fresh)
}

// fingerprint is a stable, secret-free cache key for a config: the raw secret is hashed, never held
// as a map key. A changed endpoint/region/key/secret/ssl yields a different fingerprint.
func fingerprint(cfg s3store.Config) string {
	h := sha256.New()
	// The NUL separators keep ("a","b") distinct from ("ab","").
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%t",
		cfg.Endpoint, cfg.Region, cfg.AccessKeyID, cfg.SecretKey, cfg.UseSSL)
	return hex.EncodeToString(h.Sum(nil))
}

// client returns a keep-alive-reusing client for cfg, building (and caching) one on first use.
func (c *s3Cache) client(cfg s3store.Config) (*s3store.Client, error) {
	key := fingerprint(cfg)
	if v, ok := c.clients.Load(key); ok {
		return v.(*s3store.Client), nil
	}
	cl, err := s3store.New(cfg)
	if err != nil {
		return nil, err
	}
	actual, _ := c.clients.LoadOrStore(key, cl)
	return actual.(*s3store.Client), nil
}

// listDir returns a directory's immediate folders+files, served from the short-TTL cache unless
// fresh is set. On a miss (or when fresh) it lists over the reused client and repopulates the entry.
func (c *s3Cache) listDir(ctx context.Context, cfg s3store.Config, bucket, prefix string, fresh bool) ([]string, []s3store.Object, error) {
	key := fingerprint(cfg) + "|" + bucket + "|" + prefix
	if !fresh {
		c.mu.Lock()
		e, ok := c.list[key]
		c.mu.Unlock()
		if ok && time.Now().Before(e.exp) {
			return e.folders, e.files, nil
		}
	}
	cl, err := c.client(cfg)
	if err != nil {
		return nil, nil, err
	}
	folders, files, err := cl.ListDir(ctx, bucket, prefix)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.list[key] = s3ListEntry{folders: folders, files: files, exp: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return folders, files, nil
}
