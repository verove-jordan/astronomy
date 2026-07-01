package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// emitEvery throttles progress callbacks to at most one per this many bytes (plus one per file boundary),
// so a large transfer doesn't flood the SSE stream.
const emitEvery = 1 << 20 // 1 MiB

// runUpload pushes the folder to S3. When syncOnly, it first lists the mirror and skips files already
// present at the same size.
func runUpload(ctx context.Context, client *s3store.Client, req Request, syncOnly bool, onProgress func(Progress)) (Result, error) {
	files, _, err := walkLocalFiles(req.folderDir())
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", req.folderDir(), err)
	}

	remote := map[string]int64{}
	if syncOnly {
		objs, err := client.List(ctx, req.Bucket, req.baseKey())
		if err != nil {
			return Result{}, err
		}
		for _, o := range objs {
			remote[o.Key] = o.Size
		}
	}

	// Build the upload set (and its total bytes) so the progress bar has a true denominator.
	type item struct {
		f   localFile
		key string
	}
	var todo []item
	var totalBytes int64
	skipped := 0
	for _, f := range files {
		key, err := req.keyFor(f.path)
		if err != nil {
			return Result{}, err
		}
		if syncOnly {
			if sz, ok := remote[key]; ok && sz == f.size {
				skipped++
				continue
			}
		}
		todo = append(todo, item{f: f, key: key})
		totalBytes += f.size
	}

	prog := &progressEmitter{total: totalBytes, totalFiles: len(todo), onProgress: onProgress}
	var bytes int64
	for i, it := range todo {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name, _ := filepath.Rel(req.folderDir(), it.f.path)
		prog.file(i, name, bytes)
		if err := client.Upload(ctx, req.Bucket, it.key, it.f.path, func(d int64) {
			bytes += d
			prog.bytes(i, name, bytes)
		}); err != nil {
			return Result{}, err
		}
	}
	prog.done(len(todo), bytes)
	return Result{Op: req.Op, Files: len(todo), Bytes: bytes, Skipped: skipped}, nil
}

// runDownload pulls the folder from S3, skipping objects already present locally at the same size.
func runDownload(ctx context.Context, client *s3store.Client, req Request, onProgress func(Progress)) (Result, error) {
	objs, err := client.List(ctx, req.Bucket, req.baseKey())
	if err != nil {
		return Result{}, err
	}
	type item struct {
		obj   s3store.Object
		local string
	}
	var todo []item
	var totalBytes int64
	for _, o := range objs {
		local, err := req.localFor(o.Key)
		if err != nil {
			continue // skip a key that escapes the folder
		}
		if fi, err := os.Stat(local); err == nil && fi.Size() == o.Size {
			continue // already have an identical local copy
		}
		todo = append(todo, item{obj: o, local: local})
		totalBytes += o.Size
	}

	prog := &progressEmitter{total: totalBytes, totalFiles: len(todo), onProgress: onProgress}
	var bytes int64
	for i, it := range todo {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name, _ := filepath.Rel(req.folderDir(), it.local)
		prog.file(i, name, bytes)
		if err := client.Download(ctx, req.Bucket, it.obj.Key, it.local, func(d int64) {
			bytes += d
			prog.bytes(i, name, bytes)
		}); err != nil {
			return Result{}, err
		}
	}
	prog.done(len(todo), bytes)
	return Result{Op: req.Op, Files: len(todo), Bytes: bytes}, nil
}

// runRemoveLocal deletes the folder's local files, but ONLY after verifying every one is present on S3 at
// the same size — so nothing is lost. If any file is not verified, it deletes nothing and returns an error.
func runRemoveLocal(ctx context.Context, client *s3store.Client, req Request) (Result, error) {
	files, _, err := walkLocalFiles(req.folderDir())
	if err != nil {
		return Result{}, err
	}
	for _, f := range files {
		key, err := req.keyFor(f.path)
		if err != nil {
			return Result{}, err
		}
		obj, ok, err := client.Stat(ctx, req.Bucket, key)
		if err != nil {
			return Result{}, err
		}
		if !ok || obj.Size != f.size {
			rel, _ := filepath.Rel(req.folderDir(), f.path)
			return Result{}, fmt.Errorf("remove-local aborted — %s is not backed up on S3 (nothing deleted)", rel)
		}
	}
	for _, f := range files {
		if err := os.Remove(f.path); err != nil {
			return Result{}, fmt.Errorf("remove %s: %w", f.path, err)
		}
	}
	removeEmptyDirs(req.folderDir())
	return Result{Op: req.Op, Files: len(files)}, nil
}

// removeEmptyDirs prunes now-empty directories under root (deepest first), leaving root itself.
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		if dirs[i] == root {
			continue
		}
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// progressEmitter throttles Progress callbacks (one per file boundary + at most one per emitEvery bytes).
type progressEmitter struct {
	total      int64
	totalFiles int
	onProgress func(Progress)
	lastBytes  int64
}

func (p *progressEmitter) file(idx int, name string, bytes int64) {
	p.emit(idx, name, bytes)
}

func (p *progressEmitter) bytes(idx int, name string, bytes int64) {
	if bytes-p.lastBytes >= emitEvery {
		p.emit(idx, name, bytes)
	}
}

func (p *progressEmitter) done(files int, bytes int64) {
	p.emit(files, "", bytes)
}

func (p *progressEmitter) emit(files int, name string, bytes int64) {
	p.lastBytes = bytes
	if p.onProgress != nil {
		p.onProgress(Progress{Name: name, Files: files, TotalFiles: p.totalFiles, BytesDone: bytes, BytesTotal: p.total})
	}
}
