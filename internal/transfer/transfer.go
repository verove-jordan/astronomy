// Package transfer moves a capture/result folder between the local filesystem and its S3 mirror, reporting
// byte-level progress. It encodes the mirror convention (S3 key = <keyPrefix>/<relPathInFolder>) and the
// four operations the UI exposes: upload (all), sync (only missing/changed), download, and remove-local
// (delete local copies once verified present on S3). It builds on internal/s3store for the raw S3 calls.
package transfer

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// Op is a transfer operation.
type Op string

const (
	OpUpload      Op = "upload"      // push every file in the folder
	OpSync        Op = "sync"        // push only files missing or size-changed on S3
	OpDownload    Op = "download"    // pull the folder from S3 into the local root
	OpRemoveLocal Op = "removeLocal" // delete local files, but only after verifying each is on S3
)

// Request describes one transfer of a folder between LocalRoot/RelPath and its S3 mirror under
// Bucket/KeyPrefix (KeyPrefix already includes the namespace, e.g. "<userPrefix>/data").
type Request struct {
	Op        Op
	LocalRoot string // absolute DataDir or OutputDir
	RelPath   string // folder relative to LocalRoot (slash form), e.g. "M101"
	Bucket    string
	KeyPrefix string // S3 base prefix for the namespace, e.g. "backups/data"
}

// Progress is emitted during a transfer for the byte-level progress bar.
type Progress struct {
	Name       string // current file, relative to the folder
	Files      int    // files completed
	TotalFiles int
	BytesDone  int64
	BytesTotal int64
}

// Result summarizes a finished transfer (recorded as the job result).
type Result struct {
	Op      Op    `json:"op"`
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	Skipped int   `json:"skipped,omitempty"` // sync: files already up to date
}

// Run executes req against client, reporting progress (onProgress may be nil).
func Run(ctx context.Context, client *s3store.Client, req Request, onProgress func(Progress)) (Result, error) {
	switch req.Op {
	case OpUpload:
		return runUpload(ctx, client, req, false, onProgress)
	case OpSync:
		return runUpload(ctx, client, req, true, onProgress)
	case OpDownload:
		return runDownload(ctx, client, req, onProgress)
	case OpRemoveLocal:
		return runRemoveLocal(ctx, client, req)
	default:
		return Result{}, fmt.Errorf("transfer: unknown op %q", req.Op)
	}
}

// folderDir is the absolute local folder this request operates on.
func (r Request) folderDir() string { return filepath.Join(r.LocalRoot, filepath.FromSlash(r.RelPath)) }

// baseKey is the S3 prefix mirroring folderDir (no trailing slash).
func (r Request) baseKey() string { return path.Join(r.KeyPrefix, filepath.ToSlash(r.RelPath)) }

// keyFor maps a local file (under folderDir) to its S3 key.
func (r Request) keyFor(localAbs string) (string, error) {
	rel, err := filepath.Rel(r.folderDir(), localAbs)
	if err != nil {
		return "", err
	}
	return path.Join(r.baseKey(), filepath.ToSlash(rel)), nil
}

// localFor maps an S3 key (under baseKey) back to a safe local path under folderDir, rejecting any key
// that would escape the folder (defense against crafted "../" keys — mirrors s3.go's guard).
func (r Request) localFor(key string) (string, error) {
	rel := strings.TrimPrefix(strings.TrimPrefix(key, r.baseKey()), "/")
	local := filepath.Join(r.folderDir(), filepath.FromSlash(rel))
	dir := filepath.Clean(r.folderDir())
	if local != dir && !strings.HasPrefix(local, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("transfer: key %q escapes folder", key)
	}
	return local, nil
}

// localFile is one regular file discovered under the folder.
type localFile struct {
	path string
	size int64
}

// walkLocalFiles lists every regular file under dir (skipping dotfiles and .part temp files) with its size.
func walkLocalFiles(dir string) ([]localFile, int64, error) {
	var files []localFile
	var total int64
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), ".part") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, localFile{path: p, size: info.Size()})
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return nil, 0, nil // an empty/absent folder is not an error
	}
	return files, total, err
}
