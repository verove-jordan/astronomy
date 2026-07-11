// Package localfs lists mounted external drives and browses folders on them for the "inspect an external
// drive and copy it to S3" web flow. Every path a handler accepts is funnelled through Allowed, which
// confines it to an allowlist of removable-media roots (macOS /Volumes; Linux /media, /mnt, /run/media,
// plus configured extras) and rejects symlink escapes — so the web UI can never browse the whole host disk.
// It is pure filesystem I/O (no S3, no DB) so the guard is unit-testable against a temp-dir root.
package localfs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// ErrForbidden is returned when a requested path is outside every allowed root (directly or via a symlink).
var ErrForbidden = errors.New("localfs: path is outside the allowed browse roots")

// Entry is one folder or file in a directory listing.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"` // regular files only
}

// Drive is a mounted volume the UI can browse (an immediate child of a root, e.g. /Volumes/Elements).
type Drive struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	TotalBytes uint64 `json:"total_bytes,omitempty"`
	FreeBytes  uint64 `json:"free_bytes,omitempty"`
}

// Listing is one directory level plus a Parent for up-navigation that never escapes the allowlist.
type Listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent,omitempty"` // "" when at (or just under) a root — no up-nav past the allowlist
	Entries []Entry `json:"entries"`
}

// Roots returns the allowed browse roots: the platform's removable-media mount points plus any extra roots
// from config, keeping only those that exist (cleaned + deduped). These bound every Drives/Browse/Allowed
// call, so the web UI is confined to external drives, not the whole host filesystem.
func Roots(extra []string) []string {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/Volumes")
	default: // linux and other unix
		candidates = append(candidates, "/media", "/mnt", "/run/media")
	}
	candidates = append(candidates, extra...)

	seen := map[string]bool{}
	var out []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		clean := filepath.Clean(c)
		if seen[clean] {
			continue
		}
		if fi, err := os.Stat(clean); err != nil || !fi.IsDir() {
			continue // a configured-but-missing root is silently skipped, never fatal
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

// Allowed cleans and absolutizes p, resolves symlinks, and returns the resolved absolute path plus whether
// it sits within one of roots. A blank, non-existent, or escaping path (directly or via a symlink) returns
// ok=false. This is the single chokepoint every local-FS operation goes through — resolving symlinks first
// means a link inside a drive cannot point the UI at, say, /etc.
func Allowed(roots []string, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	// EvalSymlinks requires the path to exist — fine, we only ever browse/copy real folders.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false
	}
	for _, root := range roots {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if real == rootReal || strings.HasPrefix(real, rootReal+string(os.PathSeparator)) {
			return real, true
		}
	}
	return "", false
}

// Drives lists the mounted volumes: the immediate sub-directories of each root (on macOS each is a mounted
// volume under /Volumes), name-sorted, with best-effort capacity. A root that can't be read is skipped.
func Drives(roots []string) []Drive {
	var drives []Drive
	seen := map[string]bool{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(root, e.Name())
			// Gate on Allowed (resolves symlinks + confines to the roots) so every listed drive is a real,
			// browsable directory. This drops the macOS boot-disk symlink "Macintosh HD" -> / that sits under
			// /Volumes beside the external drives: it resolves outside the allowlist, so browsing it would 403
			// anyway — better to not offer it. Real external drives are plain mount points and pass.
			real, ok := Allowed(roots, p)
			if !ok || !isDir(real) {
				continue
			}
			if seen[real] {
				continue
			}
			seen[real] = true
			total, free := diskUsage(p)
			drives = append(drives, Drive{Name: e.Name(), Path: p, TotalBytes: total, FreeBytes: free})
		}
	}
	sort.Slice(drives, func(i, j int) bool { return drives[i].Name < drives[j].Name })
	return drives
}

// Browse lists one directory level under an allowed path: sub-folders first then files, dotfiles hidden,
// each name-sorted. Parent is set only when the level above is itself allowed, so up-navigation stops at a
// root. A path outside the allowlist returns ErrForbidden.
func Browse(roots []string, p string) (Listing, error) {
	abs, ok := Allowed(roots, p)
	if !ok {
		return Listing{}, ErrForbidden
	}
	raw, err := os.ReadDir(abs)
	if err != nil {
		return Listing{}, err
	}
	var dirs, files []Entry
	for _, e := range raw {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		it := Entry{Name: e.Name(), Path: filepath.Join(abs, e.Name()), IsDir: e.IsDir()}
		if e.IsDir() {
			dirs = append(dirs, it)
			continue
		}
		if info, err := e.Info(); err == nil {
			it.Size = info.Size()
		}
		files = append(files, it)
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	listing := Listing{Path: abs, Entries: append(dirs, files...)}
	if listing.Entries == nil {
		listing.Entries = []Entry{}
	}
	if parent := filepath.Dir(abs); parent != abs {
		if _, ok := Allowed(roots, parent); ok {
			listing.Parent = parent
		}
	}
	return listing, nil
}

// isDir reports whether p is a directory, following symlinks (os.Stat) so a symlinked volume still counts.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
