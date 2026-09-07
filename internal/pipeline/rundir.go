// rundir.go reserves the scratch directory a run stacks in.
package pipeline

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// maxRunDirAttempts bounds the suffix search — far more runs than can start in the same second, so
// reaching it means the work dir is broken and failing beats spinning.
const maxRunDirAttempts = 1000

// reserveRunDir claims <workAbs>/run_<id> and returns the id it actually got with the directory it
// created.
//
// The id is a one-second timestamp and this path omits the object, so two runs started in the same
// second — even on different capture folders, which share no target lock — landed on the same
// directory and overwrote each other's per-channel sequences. os.Mkdir is atomic: the first caller
// wins the plain id, the rest take the next free suffix.
func reserveRunDir(workAbs, runID string) (id, dir string, err error) {
	if err := fsutil.EnsureDir(workAbs); err != nil {
		return "", "", fmt.Errorf("work dir %s: %w", workAbs, err)
	}
	for i := 0; i < maxRunDirAttempts; i++ {
		id := runID
		if i > 0 {
			id = fmt.Sprintf("%s_%d", runID, i+1)
		}
		dir := filepath.Join(workAbs, "run_"+id)
		switch err := os.Mkdir(dir, 0o755); {
		case err == nil:
			return id, dir, nil
		case errors.Is(err, fs.ErrExist):
			continue // taken by a concurrent run (or a leftover) — try the next suffix
		default:
			return "", "", fmt.Errorf("reserve run dir %s: %w", dir, err)
		}
	}
	return "", "", fmt.Errorf("reserve run dir under %s: %d ids already taken for %q", workAbs, maxRunDirAttempts, runID)
}
