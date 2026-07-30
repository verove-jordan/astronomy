// Package transfer moves a capture/result folder between the local filesystem and its S3 mirror, reporting
// byte-level progress. It encodes the mirror convention (S3 key = <keyPrefix>/<relPathInFolder>) and the
// four operations the UI exposes: upload (all), sync (only missing/changed), download, and remove-local
// (delete local copies once verified present on S3). It builds on internal/s3store for the raw S3 calls.
package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// ErrPaused is returned when a transfer stops because a manual pause was requested (checked between
// files). It is distinct from context.Canceled (a cancel) so the job layer can park the job in the
// resumable paused state instead of failing it; a resumed transfer re-runs with sync/size-skip
// semantics, so already-copied files are not re-transferred.
var ErrPaused = errors.New("transfer paused")

// ArchivedError is returned by a download when one or more objects in the pull set are in an archived
// storage class (Glacier Flexible / Deep Archive) and not yet restored — a GET would fail with
// InvalidObjectState. The engine detects this up front (pre-flight, before streaming anything) and
// surfaces the offending keys so the job layer can initiate a thaw and park the job until the objects
// are readable, then resume the pull. Distinct from a hard error so a run is never failed by cold inputs.
type ArchivedError struct{ Keys []string }

func (e *ArchivedError) Error() string {
	return fmt.Sprintf("transfer: %d archived object(s) need a Glacier restore before download", len(e.Keys))
}

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
	// Verify upgrades a sync's skip decision from size-only to byte-for-byte: a file already on S3 at the
	// same size is re-checked against its content MD5 (verifyMirrored) and only skipped when it matches, so
	// a same-size-but-corrupted or half-written mirror object is re-uploaded instead of trusted. It costs a
	// Stat + a local MD5 per already-present file, so it is opt-in; it has no effect on OpUpload (which
	// pushes everything anyway) — only on OpSync. Default false keeps the fast immutable-capture semantics.
	Verify bool
	// PauseRequested, when set, is polled between files: a true return stops the transfer with ErrPaused
	// (a resumable manual pause), so a long S3 copy can be paused between files. nil → never pauses.
	PauseRequested func() bool
	// Plan, when set, overrides the mirror key of each file with a precomputed classified key (a file's
	// Rel → its full S3 Key). It is the seam for the classified darks/offsets/flats/lum layout, which is
	// not derivable from the local path. nil → the byte-for-byte legacy mirror (<KeyPrefix>/<rel>). The
	// job layer computes the plan (it has the DB) so this package stays I/O-free and testable.
	Plan []PlannedFile
	// OnStored is called after each file is uploaded or skipped-as-already-present, with its Rel, the S3
	// Key it lives at, and its size — so the job layer can persist the local-rel → key mapping (the ledger
	// that recovers the non-reversible classified keys). nil → not recorded. It (and onProgress) may be
	// called from multiple worker goroutines but the engine SERIALIZES those calls, so it need not lock.
	OnStored func(rel, key string, size int64)
	// Concurrency bounds how many files upload/download in PARALLEL, saturating the link instead of idling
	// on each file's round-trip. <= 0 → defaultConcurrency; clamped to maxConcurrency. Per-file retries and
	// the pause/verify/skip semantics are unchanged — only the fan-out is new.
	Concurrency int
	// PlannedOnly restricts an OpRemoveLocal to the files named in Plan (by their Rel): only those are
	// verified and deleted, every other local file in the folder is left untouched. It backs the low-disk
	// staged free, which downloaded and now frees only ONE frame-type/channel set at a time while the rest
	// of the capture folder is still needed. Requires Plan; with no plan it frees nothing (safe). Default
	// false keeps the whole-folder semantics every existing caller relies on. Only affects OpRemoveLocal.
	PlannedOnly bool
	// ExcludeDirs names subdirectories (by directory name, at any depth) that the walk skips entirely — so
	// their files are neither uploaded, downloaded-over, nor removed. It backs the calibration-library
	// mirror, whose LibraryDir also holds a multi-GB Gaia `catalogues/` tree that is not a master and must
	// never be mirrored. Empty → walk everything (existing behaviour).
	ExcludeDirs []string
	// SkipSymlinks drops symlinked files/dirs from the walk. filepath.WalkDir uses Lstat, so a symlink is
	// reported with the link's own (tiny) size while os.Open on it streams the target's full content —
	// ballooning the upload and corrupting byte accounting. WorkDir is full of Siril `link` symlinks to the
	// input frames, so the local-folder copy sets this. Empty → walk everything (existing behaviour).
	SkipSymlinks bool
}

// excluded reports whether a directory name is in ExcludeDirs.
func (r Request) excluded(name string) bool {
	for _, d := range r.ExcludeDirs {
		if d == name {
			return true
		}
	}
	return false
}

// Transfer concurrency: the number of files moved in parallel. The default keeps a fat pipe busy for the
// mixed medium-FITS + tiny-sidecar astro workload without oversubscribing a slow source drive (parallel
// reads from one spinning/USB disk can thrash); the ceiling guards against a pathological override.
const (
	defaultConcurrency = 6
	maxConcurrency     = 32
)

// concurrency is the effective parallel-file count for this request.
func (r Request) concurrency() int {
	c := r.Concurrency
	if c <= 0 {
		c = defaultConcurrency
	}
	if c > maxConcurrency {
		c = maxConcurrency
	}
	return c
}

// PlannedFile is one entry of a classified transfer plan: a file's folder-relative slash path and the
// full S3 key it maps to (plus its size, for the download same-size skip).
type PlannedFile struct {
	Rel  string
	Key  string
	Size int64
}

// paused reports whether a manual pause has been requested for this transfer.
func (r Request) paused() bool { return r.PauseRequested != nil && r.PauseRequested() }

// planMap builds a Rel → Key lookup from the plan (nil when there is no plan → legacy keys).
func (r Request) planMap() map[string]string {
	if r.Plan == nil {
		return nil
	}
	m := make(map[string]string, len(r.Plan))
	for _, pf := range r.Plan {
		m[pf.Rel] = pf.Key
	}
	return m
}

// keyForRel maps a folder-relative slash path to its S3 key: the plan's classified key when present, else
// the legacy mirror key. pm is the precomputed plan map (nil = legacy).
func (r Request) keyForRel(relSlash string, pm map[string]string) string {
	if pm != nil {
		if k, ok := pm[relSlash]; ok {
			return k
		}
	}
	return path.Join(r.baseKey(), relSlash)
}

// relOf returns localAbs's folder-relative slash path.
func (r Request) relOf(localAbs string) (string, error) {
	rel, err := filepath.Rel(r.folderDir(), localAbs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// recordStored reports a stored file to OnStored (the mapping ledger), if a callback is set.
func (r Request) recordStored(rel, key string, size int64) {
	if r.OnStored != nil {
		r.OnStored(rel, key, size)
	}
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
	// Warnings surface non-fatal caveats to the job result — today only remove-local's "verified by
	// size only (legacy upload): <rel>" for pre-MD5-metadata multipart objects.
	Warnings []string `json:"warnings,omitempty"`
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
	return r.localForRel(rel)
}

// localForRel maps a folder-relative slash path to a safe absolute local path under folderDir, rejecting
// any rel that would escape the folder (a plan rel comes from our own DB, but guard defensively anyway).
func (r Request) localForRel(rel string) (string, error) {
	local := filepath.Join(r.folderDir(), filepath.FromSlash(rel))
	dir := filepath.Clean(r.folderDir())
	if local != dir && !strings.HasPrefix(local, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("transfer: rel %q escapes folder", rel)
	}
	return local, nil
}

// localFile is one regular file discovered under the folder.
type localFile struct {
	path string
	size int64
}

// walkLocalFiles lists every regular file under dir (skipping dotfiles, .part temp files, and any
// excludeDirs subtree) with its size. When skipSymlinks is set, symlinked entries are dropped too: WalkDir
// reports a symlink via Lstat (link-sized, never descended), but the uploader os.Opens it and streams the
// full target — so following work/ `link` frames would balloon the copy and corrupt byte accounting.
func walkLocalFiles(dir string, excludeDirs []string, skipSymlinks bool) ([]localFile, int64, error) {
	excl := make(map[string]bool, len(excludeDirs))
	for _, d := range excludeDirs {
		excl[d] = true
	}
	var files []localFile
	var total int64
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipSymlinks && d.Type()&os.ModeSymlink != 0 {
			return nil // a symlink to a dir is never descended (Lstat type is ModeSymlink), so this is enough
		}
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(d.Name(), ".") || excl[d.Name()]) {
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
