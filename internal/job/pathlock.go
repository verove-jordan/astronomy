package job

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// pathLocker serializes jobs that touch overlapping input roots. Conflict = the same path OR a
// directory-ancestor relationship — so a transfer over data/M101 and a run reading data/M101/lights
// serialize (the old exact-path mutex map let them race). All-or-nothing acquisition under one
// monitor makes it deadlock-free regardless of acquisition order, and the held map only ever
// contains actively-running roots (the old map grew one mutex per path forever).
type pathLocker struct {
	mu   sync.Mutex
	cond *sync.Cond
	held map[string]int
}

func newPathLocker() *pathLocker {
	l := &pathLocker{held: map[string]int{}}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// Acquire blocks until EVERY requested path is conflict-free, claims them, and returns the release.
func (l *pathLocker) Acquire(paths []string) (release func()) {
	roots := normalizeRoots(paths)
	l.mu.Lock()
	for l.anyConflict(roots) {
		l.cond.Wait()
	}
	for _, r := range roots {
		l.held[r]++
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		for _, r := range roots {
			if l.held[r]--; l.held[r] <= 0 {
				delete(l.held, r)
			}
		}
		l.mu.Unlock()
		l.cond.Broadcast()
	}
}

func (l *pathLocker) anyConflict(roots []string) bool {
	for _, r := range roots {
		for h := range l.held {
			if pathsConflict(r, h) {
				return true
			}
		}
	}
	return false
}

// pathsConflict reports whether two cleaned paths are equal or one contains the other.
func pathsConflict(a, b string) bool {
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}

// normalizeRoots cleans, dedupes and sorts the requested roots (determinism; empties dropped).
func normalizeRoots(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		c := filepath.Clean(p)
		if c == "" || c == "." || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
