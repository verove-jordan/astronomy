package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/libmirror"
	"github.com/verove-jordan/astronomy/internal/s3store"
)

// libS3 is the slice of the S3 client the puller needs (stat an object, restore it, download it) — an
// interface so the pull/free logic is unit-testable with a fake. *s3store.Client satisfies it.
type libS3 interface {
	Stat(ctx context.Context, bucket, key string) (s3store.Object, bool, error)
	Restore(ctx context.Context, bucket, key string, days int, tier s3store.RestoreTier) error
	Download(ctx context.Context, bucket, key, localPath string, onBytes func(delta int64)) error
}

// s3LibPuller implements libmirror.Puller: it downloads matched calibration masters from the S3 library
// mirror (<prefix>/library/<file>) when they are absent locally, and frees the transiently-pulled copies
// after the run. It is concurrency-safe: channels calibrate in parallel, so Ensure may run from several
// goroutines. Every failure is soft — a master that is missing from the mirror, or fails to download, just
// leaves the caller's existing local / rebuild fallback in place; a run never fails because of the mirror.
type s3LibPuller struct {
	client libS3
	bucket string
	prefix string // the user prefix; keys live at <prefix>/library/<rel>
	libDir string // absolute library dir; a master under it maps to its mirror key

	mu       sync.Mutex
	pulled   []string // local paths THIS run downloaded (freed after the run)
	pulledN  int
	freedN   int
	warnings []string
}

func (p *s3LibPuller) Ensure(ctx context.Context, localPaths []string) error {
	for _, lp := range localPaths {
		if lp == "" {
			continue
		}
		key := libmirror.KeyFor(p.prefix, p.libDir, lp)
		if key == "" {
			continue // not a file under the library dir
		}
		if _, err := os.Stat(lp); err == nil {
			continue // already present locally — the mirror is kept, so this is the common case
		}
		// Only pull what the mirror actually holds; a missing object is not an error (the caller falls back).
		obj, ok, err := p.client.Stat(ctx, p.bucket, key)
		if err != nil || !ok {
			continue
		}
		// A master archived to Glacier can't be downloaded until restored. Kick off its thaw (best-effort,
		// idempotent) so a LATER run finds it warm, and fall back to the local rebuild this run — a run never
		// blocks on a cold master. An already-restored (RestoreReady) master downloads normally below.
		if obj.Archived() && !obj.RestoreReady() {
			if !obj.RestorePending() {
				_ = p.client.Restore(ctx, p.bucket, key, 0, s3store.TierStandard)
			}
			p.warn(fmt.Sprintf("library %s is archived — restoring from Glacier; used local rebuild this run", filepath.Base(lp)))
			continue
		}
		if err := fsutil.EnsureDir(filepath.Dir(lp)); err != nil {
			p.warn(fmt.Sprintf("library pull %s: %v", filepath.Base(lp), err))
			continue
		}
		if err := p.client.Download(ctx, p.bucket, key, lp, nil); err != nil {
			p.warn(fmt.Sprintf("library pull %s: %v", filepath.Base(lp), err))
			continue
		}
		p.mu.Lock()
		p.pulled = append(p.pulled, lp)
		p.pulledN++
		p.mu.Unlock()
	}
	return nil
}

func (p *s3LibPuller) FreePulled(_ context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, lp := range p.pulled {
		if err := os.Remove(lp); err == nil {
			p.freedN++
		}
	}
	p.pulled = nil
}

func (p *s3LibPuller) Notes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var notes []string
	if p.pulledN > 0 {
		notes = append(notes, fmt.Sprintf("S3 library mirror: pulled %d master(s), freed %d after the run", p.pulledN, p.freedN))
	}
	return append(notes, p.warnings...)
}

func (p *s3LibPuller) warn(s string) {
	p.mu.Lock()
	p.warnings = append(p.warnings, s)
	p.mu.Unlock()
}

// libPuller builds the per-run library-mirror puller. It is a no-op (libmirror.Nop) unless the library has
// been copied to S3 (its mirror location is recorded) AND an S3 client can be built — so a run with no
// mirror, or no S3, stays byte-identical to before. Never fatal: any failure yields the Nop puller.
func (m *Manager) libPuller(ctx context.Context) libmirror.Puller {
	loc, ok, err := m.store.LibraryMirror(ctx)
	if err != nil || !ok {
		return libmirror.Nop{}
	}
	client, err := m.s3Client(ctx)
	if err != nil {
		return libmirror.Nop{}
	}
	libDir, err := filepath.Abs(m.cfg.LibraryDir)
	if err != nil {
		return libmirror.Nop{}
	}
	return &s3LibPuller{client: client, bucket: loc.Bucket, prefix: loc.Prefix, libDir: libDir}
}
