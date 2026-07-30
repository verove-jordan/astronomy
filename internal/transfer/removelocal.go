package transfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/s3store"
)

// runRemoveLocal deletes the folder's local files, but ONLY after verifying every one is safely mirrored
// on S3 — strong content equality where possible (see verifyMirrored), so a same-size-different-bytes
// object can never cost the only good copy. If ANY file fails verification, nothing is deleted. Files
// that could only be verified by size (legacy multipart uploads without our MD5 metadata) are still
// deleted, but each is reported in Result.Warnings.
func runRemoveLocal(ctx context.Context, client s3API, req Request) (Result, error) {
	files, _, err := walkLocalFiles(req.folderDir(), req.ExcludeDirs, req.SkipSymlinks)
	if err != nil {
		return Result{}, err
	}
	pm := req.planMap()
	if req.PlannedOnly { // staged free: verify + delete only this wave's files (named in the plan)
		files = filterPlanned(files, req, pm)
	}
	var warnings []string
	for _, f := range files {
		rel, err := req.relOf(f.path)
		if err != nil {
			return Result{}, err
		}
		// Verify against the classified key when the plan has one, else the legacy mirror key.
		key := req.keyForRel(rel, pm)
		ok, legacy, err := verifyMirrored(ctx, client, req.Bucket, key, f)
		if err != nil {
			return Result{}, err
		}
		if !ok {
			return Result{}, fmt.Errorf("remove-local aborted — %s is not safely backed up on S3 (nothing deleted)", rel)
		}
		if legacy {
			warnings = append(warnings, "verified by size only (legacy upload): "+rel)
		}
	}
	for _, f := range files {
		if req.paused() { // deletion is idempotent — a resumed remove-local re-verifies and deletes the rest
			return Result{}, ErrPaused
		}
		if err := ctx.Err(); err != nil {
			return Result{}, fmt.Errorf("remove-local interrupted: %w", err)
		}
		if err := os.Remove(f.path); err != nil {
			return Result{}, fmt.Errorf("remove %s: %w", f.path, err)
		}
	}
	removeEmptyDirs(req.folderDir())
	return Result{Op: req.Op, Files: len(files), Warnings: warnings}, nil
}

// filterPlanned keeps only the files whose folder-relative path is named in the plan map — the staged
// remove-local (PlannedOnly) frees just the current wave's files and leaves the rest of the folder. With
// no plan it returns nothing, so an unplanned file is never deleted.
func filterPlanned(files []localFile, req Request, pm map[string]string) []localFile {
	if pm == nil {
		return nil
	}
	out := make([]localFile, 0, len(files))
	for _, f := range files {
		rel, err := req.relOf(f.path)
		if err != nil {
			continue
		}
		if _, ok := pm[rel]; ok {
			out = append(out, f)
		}
	}
	return out
}

// verifyMirrored reports whether local file f is safely mirrored at bucket/key, in tiers of strength:
//
//  1. the object must exist at the same size (else not ok);
//  2. if the object carries our content-MD5 user metadata (every Upload writes it), compare it to the
//     local file's MD5 — a mismatch is not ok;
//  3. else if the ETag has no "-" it is a single-part upload, whose ETag IS the content MD5 — compare;
//  4. else it is a legacy multipart object without metadata: accept on size alone (plus a sanity check
//     that the local file was not modified after the upload, when both mtimes are known) and report
//     legacy=true so the caller can warn.
func verifyMirrored(ctx context.Context, client s3API, bucket, key string, f localFile) (ok bool, legacy bool, err error) {
	obj, exists, err := client.Stat(ctx, bucket, key)
	if err != nil {
		return false, false, err
	}
	if !exists || obj.Size != f.size {
		return false, false, nil
	}
	if obj.MD5 != "" {
		sum, err := s3store.MD5File(f.path)
		if err != nil {
			return false, false, err
		}
		return strings.EqualFold(sum, obj.MD5), false, nil
	}
	if obj.ETag != "" && !strings.Contains(obj.ETag, "-") {
		sum, err := s3store.MD5File(f.path)
		if err != nil {
			return false, false, err
		}
		return strings.EqualFold(sum, obj.ETag), false, nil
	}
	if fi, err := os.Stat(f.path); err == nil && obj.ModTime > 0 && fi.ModTime().UnixMilli() > obj.ModTime {
		return false, false, nil // local file changed after the upload — the mirror may be stale
	}
	return true, true, nil
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
