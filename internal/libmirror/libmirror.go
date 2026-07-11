// Package libmirror encodes the calibration-master library's S3 mirror convention and the consumer-side
// Puller the pipeline uses to fetch a matched master back on demand.
//
// The persistent library (internal/calib + internal/nightscape) is a set of flat FITS master files under
// LibraryDir — master_*.fits / phone_master_*.fits (+ a .sig reuse sidecar each). A "Copy library to S3"
// action mirrors those files to <userPrefix>/library/<file>; when a later run MATCHES a master whose local
// file is absent (the library is kept as a synced mirror, but a given machine may not hold every file), the
// Puller downloads just that master from the mirror, the run uses it, and the transiently-pulled copy is
// freed afterwards. The multi-GB Gaia `catalogues/` subtree under LibraryDir is NOT a calibration master and
// is never mirrored. This package is pure (interface + path helpers); the S3-backed Puller lives in
// internal/job so the pipeline stays I/O-free and testable, exactly like the input Catalog/Stager seams.
package libmirror

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// LibraryRoot is the fixed S3 key segment the library mirrors under, beside data/ output/ backup/.
const LibraryRoot = "library"

// CatalogueDir is the LibraryDir subdirectory (the multi-GB Gaia astrometry catalogues) that is NOT a
// calibration master and is excluded from the S3 mirror.
const CatalogueDir = "catalogues"

// Puller supplies calibration-master files on demand from the S3 library mirror. The Nop puller (no S3
// configured) makes every call a no-op, so the local-only path stays byte-identical.
type Puller interface {
	// Ensure downloads any of the given library master files that are ABSENT locally from the S3 mirror. A
	// file already present locally, or absent from the mirror, is left as-is and is never fatal: the caller
	// keeps whatever local / soft-fail behaviour it had. Paths outside the library dir are ignored.
	Ensure(ctx context.Context, localPaths []string) error
	// FreePulled deletes the master files THIS puller downloaded this run (they remain on S3), reclaiming the
	// transient local space; files that were already local are untouched. Never fatal.
	FreePulled(ctx context.Context)
	// Notes returns end-of-run observability lines (masters pulled / freed) for run.json.
	Notes() []string
}

// Nop is the no-S3 puller: every method is a no-op. Used whenever no S3 mirror is available.
type Nop struct{}

func (Nop) Ensure(context.Context, []string) error { return nil }
func (Nop) FreePulled(context.Context)             {}
func (Nop) Notes() []string                        { return nil }

// KeyFor maps a local master file (under libDir) to its S3 mirror key <userPrefix>/library/<relSlash>.
// localPath need not exist — it is a pure key computation. Returns "" when localPath is not under libDir
// (so a caller can skip a path that isn't a library file).
func KeyFor(userPrefix, libDir, localPath string) string {
	rel, err := filepath.Rel(libDir, localPath)
	if err != nil || rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
		return ""
	}
	return path.Join(userPrefix, LibraryRoot, filepath.ToSlash(rel))
}

// IsMasterFile reports whether a base filename is a library master (or one of its sidecars) we
// mirror: the flat master_* / phone_master_* FITS + their .sig reuse signature and _defects.lst
// bad-pixel map, NOT catalogues or any other library content.
func IsMasterFile(name string) bool {
	if strings.HasPrefix(name, "master_") || strings.HasPrefix(name, "phone_master_") {
		return strings.HasSuffix(name, ".fits") || strings.HasSuffix(name, ".sig") ||
			strings.HasSuffix(name, "_defects.lst")
	}
	return false
}

// MasterFiles lists the flat library master files under libDir (absolute paths), skipping subdirs — so the
// multi-GB catalogues/ tree is never included. os.ReadDir returns entries sorted by name, so the result is
// deterministic. A missing libDir yields no files (not an error): there is simply nothing to mirror.
func MasterFiles(libDir string) ([]string, error) {
	entries, err := os.ReadDir(libDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !IsMasterFile(e.Name()) {
			continue
		}
		out = append(out, filepath.Join(libDir, e.Name()))
	}
	return out, nil
}
