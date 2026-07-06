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
func runUpload(ctx context.Context, client s3API, req Request, syncOnly bool, onProgress func(Progress)) (Result, error) {
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
			// Size-only skip BY DESIGN: captures are immutable once written, so an equal-size object is
			// the same file — the worst case is keeping a stale copy, never losing data (remove-local is
			// where strong verification matters; see verifyMirrored).
			if sz, ok := remote[key]; ok && sz == f.size {
				skipped++
				continue
			}
		}
		todo = append(todo, item{f: f, key: key})
		totalBytes += f.size
	}

	prog := &progressEmitter{total: totalBytes, totalFiles: len(todo), onProgress: onProgress}
	var base int64 // bytes of fully transferred files — advanced only on success
	for i, it := range todo {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name, _ := filepath.Rel(req.folderDir(), it.f.path)
		prog.file(i, name, base)
		var fileBytes int64
		err := retryFile(ctx, func() error {
			fileBytes = 0 // a retried attempt restreams the file from zero — never double-count progress
			return client.Upload(ctx, req.Bucket, it.key, it.f.path, func(d int64) {
				fileBytes += d
				prog.bytes(i, name, base+fileBytes)
			})
		})
		if err != nil {
			return Result{}, err
		}
		base += it.f.size
	}
	prog.done(len(todo), base)
	return Result{Op: req.Op, Files: len(todo), Bytes: base, Skipped: skipped}, nil
}

// runDownload pulls the folder from S3, skipping objects already present locally at the same size.
func runDownload(ctx context.Context, client s3API, req Request, onProgress func(Progress)) (Result, error) {
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
		// Size-only skip BY DESIGN (mirrors the sync skip above): immutable captures make an equal-size
		// local file the same file; the risk is a stale copy, not data loss, and hashing every present
		// file would make re-downloads as slow as downloads.
		if fi, err := os.Stat(local); err == nil && fi.Size() == o.Size {
			continue // already have an identical local copy
		}
		todo = append(todo, item{obj: o, local: local})
		totalBytes += o.Size
	}

	prog := &progressEmitter{total: totalBytes, totalFiles: len(todo), onProgress: onProgress}
	var base int64 // bytes of fully transferred files — advanced only on success
	for i, it := range todo {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name, _ := filepath.Rel(req.folderDir(), it.local)
		prog.file(i, name, base)
		var fileBytes int64
		err := retryFile(ctx, func() error {
			fileBytes = 0 // a retried attempt restreams the file from zero — never double-count progress
			return client.Download(ctx, req.Bucket, it.obj.Key, it.local, func(d int64) {
				fileBytes += d
				prog.bytes(i, name, base+fileBytes)
			})
		})
		if err != nil {
			return Result{}, err
		}
		base += it.obj.Size
	}
	prog.done(len(todo), base)
	return Result{Op: req.Op, Files: len(todo), Bytes: base}, nil
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
