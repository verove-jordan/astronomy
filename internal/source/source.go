// Package source abstracts where live-stacking capture frames arrive from. A Source lists the
// candidate frame objects cheaply (name + size + mtime, no content read) and materializes any object
// as a local file, because the downstream engine (Siril) only operates on local paths. A local
// directory is served in place; a remote object store (S3) is mirrored into a local download dir on
// first fetch. The live watcher diffs successive List() snapshots to find newly-arrived frames.
package source

import "context"

// Object is a candidate frame in a source: a stable logical key plus the cheap stat metadata the
// watcher uses to detect new and still-being-written files.
type Object struct {
	Key     string // stable identity (local absolute path, or S3 object key)
	Size    int64  // bytes
	ModTime int64  // last-modified, unix milliseconds
}

// Source is a pollable origin of capture frames for a live-stacking session.
type Source interface {
	// List returns the current candidate frame objects. It must be cheap (no content reads) since the
	// watcher calls it on every poll.
	List(ctx context.Context) ([]Object, error)

	// Fetch ensures a local copy of o exists and returns its local path. A local source returns the
	// existing path unchanged; a remote source downloads o into its local root on first call.
	Fetch(ctx context.Context, o Object) (string, error)

	// LocalRoot is the directory that holds every materialized frame — the directory the pipeline
	// inspects each batch and finalizes over. For a local source this is the watched directory itself.
	LocalRoot() string

	// Close releases any resources (a remote client connection); local sources no-op.
	Close() error
}
