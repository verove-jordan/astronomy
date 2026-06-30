package source

import (
	"context"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// LocalSource serves frames from a watched directory in place (no copying). It is the common case:
// the capture software writes subs into a folder on the same host as the engine.
type LocalSource struct {
	root string
}

// NewLocal returns a LocalSource rooted at dir (resolved to an absolute path so listed keys are
// absolute and directly stattable).
func NewLocal(dir string) (*LocalSource, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &LocalSource{root: abs}, nil
}

// List walks the root for FITS and raw frames and stats each. A not-yet-created root is reported as
// empty rather than an error, so a session may start before the first sub lands.
func (s *LocalSource) List(ctx context.Context) ([]Object, error) {
	if _, err := os.Stat(s.root); os.IsNotExist(err) {
		return nil, nil
	}
	fits, err := inspect.ListFITSFrames(s.root)
	if err != nil {
		return nil, err
	}
	raw, err := inspect.ListRawFrames(s.root)
	if err != nil {
		return nil, err
	}
	paths := append(fits, raw...)
	out := make([]Object, 0, len(paths))
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			continue // vanished between walk and stat; skip
		}
		out = append(out, Object{Key: p, Size: info.Size(), ModTime: info.ModTime().UnixMilli()})
	}
	return out, nil
}

// Fetch is a no-op for a local source: the key already is the local path.
func (s *LocalSource) Fetch(ctx context.Context, o Object) (string, error) {
	return o.Key, nil
}

// LocalRoot is the watched directory itself.
func (s *LocalSource) LocalRoot() string { return s.root }

// Close releases nothing for a local source.
func (s *LocalSource) Close() error { return nil }
