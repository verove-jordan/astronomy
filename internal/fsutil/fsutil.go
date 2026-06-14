// Package fsutil holds small filesystem helpers shared by the pipeline and calibration stages.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureDir creates dir (and parents) if needed.
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// LinkFrames symlinks each source file into destDir (created if needed), prefixing names with a
// zero-padded index so Siril's alphabetical sequence ordering matches acquisition order.
// Existing links of the same name are replaced. Returns the number of links created.
func LinkFrames(destDir string, paths []string) (int, error) {
	if err := EnsureDir(destDir); err != nil {
		return 0, err
	}
	for i, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return i, err
		}
		link := filepath.Join(destDir, fmt.Sprintf("%05d_%s", i+1, filepath.Base(p)))
		_ = os.Remove(link)
		if err := os.Symlink(abs, link); err != nil {
			return i, fmt.Errorf("symlink %s: %w", p, err)
		}
	}
	return len(paths), nil
}
