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

// maxRunDirAttempts bounds the suffix search. A run id has one-second resolution, so this is far more
// concurrent runs than the worker pool can ever start in the same second; hitting it means something
// is wrong (an unwritable work dir that keeps reporting "exists"), and failing loudly beats spinning.
const maxRunDirAttempts = 1000

// reserveRunDir claims <workAbs>/run_<id> for this run and returns the id it actually got together
// with the directory it created.
//
// The id is a one-second timestamp and this scratch path deliberately does NOT include the object, so
// two runs started within the same second — even on different capture folders, which take no shared
// target lock — used to land on the same <work>/run_<id> and overwrite each other's per-channel
// sequences mid-stack. os.Mkdir is atomic, so the first caller wins the plain id and every other one
// takes the next free suffix; the returned id is what the output directory must be named after, so
// the two stay in step.
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
