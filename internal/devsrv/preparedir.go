package devsrv

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Creating the capture folder, on the host, where the frames are actually written.
//
// The engine used to create it itself, which broke the case that matters most for an external drive:
// a CONTAINERIZED engine mounts /Volumes read-only (deliberately — it only ever reads from drives for
// the copy-to-S3 feature), so `mkdir /Volumes/Elements/…` failed with "read-only file system" even
// though the device server, running natively, could write there perfectly well.
//
// The right owner is the process that writes the files. This one does: it already creates each
// frame's parent directory on save. Doing it up front as well means an unwritable destination is
// reported when the run is STARTED rather than after the first exposure has already been taken.

// readOnlyHint explains a read-only failure, naming the filesystem when that is the real cause.
//
// macOS can READ an NTFS volume but never write to it, so a Windows-formatted drive shows up as a
// normal folder that silently refuses every write. Relaying the bare "read-only file system" sends
// people looking at permissions, Docker mounts or the app — none of which can fix it. Only
// reformatting (exFAT works read-write on macOS and Windows) or a third-party NTFS driver will.
func readOnlyHint(dir string, err error) string {
	if !errors.Is(err, syscall.EROFS) && !strings.Contains(err.Error(), "read-only file system") {
		return ""
	}
	switch strings.ToLower(filesystemType(existingAncestor(dir))) {
	case "ntfs":
		return " — this drive is NTFS, which macOS can read but never write. Reformat it to exFAT " +
			"(read-write on both macOS and Windows) or install an NTFS driver, then try again"
	case "":
		return " — the volume is mounted read-only"
	case "hfs", "apfs", "exfat", "msdos", "vfat":
		return " — the volume is mounted read-only; on macOS, ejecting and reconnecting the drive " +
			"often clears a read-only mount left by an unclean unplug"
	default:
		return " — the volume is mounted read-only"
	}
}

// existingAncestor walks up to the nearest path that exists, so the filesystem can be identified even
// when the requested folder is the thing that could not be created.
func existingAncestor(dir string) string {
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// prepareDir creates a capture folder and proves it is writable. POST /prepare-dir
func (s *Server) prepareDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Path == "" || !filepath.IsAbs(body.Path) {
		http.Error(w, "an absolute path is required", http.StatusBadRequest)
		return
	}
	if err := ensureWritableDir(body.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(), "code": "capture_dir_unwritable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": body.Path, "writable": true})
}

// ensureWritableDir creates dir if needed and verifies a file can actually be created in it.
//
// The probe matters beyond tidiness: a folder that already exists on a read-only mount, or on a drive
// that has filled up, passes MkdirAll and then fails on the first frame — an hour into the night.
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create the capture folder: %w%s", err, readOnlyHint(dir, err))
	}
	probe, err := os.CreateTemp(dir, ".astrostack-write-probe-*")
	if err != nil {
		return fmt.Errorf("the capture folder is not writable: %w%s", err, readOnlyHint(dir, err))
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}
