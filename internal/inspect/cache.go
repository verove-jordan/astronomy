package inspect

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// ScanCache speeds up re-inspecting a growing set of capture folders. It remembers each directory's
// scan keyed by the directory's modification time, so adding a folder and inspecting again only scans
// the new (or changed) folders instead of re-reading every FITS header again. Safe for concurrent use.
//
// It must only be used with detection-default options (no FilterMapping): cached frames are shared
// read-only across calls, and a filter override would mutate them in place. The inspect and reuse-
// preview paths both use DefaultScanOptions, so this holds.
type ScanCache struct {
	mu      sync.Mutex
	entries map[string]scanCacheEntry
}

type scanCacheEntry struct {
	mtime     int64
	frames    []*Frame
	videos    []*Frame
	warnings  []string
	detection *ChannelDetection
}

// maxCacheEntries bounds the cache; a single capture session inspects far fewer dirs than this, so the
// crude reset on overflow effectively never fires in practice.
const maxCacheEntries = 1024

// NewScanCache returns an empty cache.
func NewScanCache() *ScanCache {
	return &ScanCache{entries: map[string]scanCacheEntry{}}
}

// ScanMany scans roots into one merged Inventory, reusing the cached per-directory scan of any folder
// whose modification time is unchanged. The output is identical to package-level ScanMany.
func (c *ScanCache) ScanMany(ctx context.Context, roots []string, opts ScanOptions) (*Inventory, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("scan: no directories given")
	}
	root := roots[0]
	if len(roots) > 1 {
		root = commonParent(roots)
	}
	merged := &Inventory{Root: root}
	for _, r := range roots {
		e, err := c.scanDir(ctx, r, opts)
		if err != nil {
			return nil, err
		}
		merged.Frames = append(merged.Frames, e.frames...)
		merged.Videos = append(merged.Videos, e.videos...)
		merged.Warnings = append(merged.Warnings, e.warnings...)
		if merged.ChannelDetection == nil {
			merged.ChannelDetection = e.detection
		}
	}
	finalizeInventory(merged, opts)
	return merged, nil
}

// scanDir returns one directory's scan, from cache when its mtime is unchanged, else scanning afresh.
func (c *ScanCache) scanDir(ctx context.Context, root string, opts ScanOptions) (scanCacheEntry, error) {
	mt := dirMTime(root)
	c.mu.Lock()
	e, ok := c.entries[root]
	c.mu.Unlock()
	if ok && e.mtime == mt && mt != 0 {
		return e, nil // unchanged since last scan — reuse it
	}

	inv, err := scanFrames(ctx, root, opts)
	if err != nil {
		return scanCacheEntry{}, err
	}
	e = scanCacheEntry{
		mtime:     mt,
		frames:    inv.Frames,
		videos:    inv.Videos,
		warnings:  inv.Warnings,
		detection: inv.ChannelDetection,
	}
	c.mu.Lock()
	if len(c.entries) >= maxCacheEntries {
		c.entries = map[string]scanCacheEntry{}
	}
	c.entries[root] = e
	c.mu.Unlock()
	return e, nil
}

// dirMTime is the directory's modification time (Unix nanos), or 0 when it can't be stat'd. A capture
// folder's mtime changes when frames are added or removed, which invalidates its cache entry.
func dirMTime(dir string) int64 {
	fi, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}
