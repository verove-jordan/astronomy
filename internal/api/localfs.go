package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/localfs"
)

// browseRoots resolves the external-drive roots the UI may browse: the platform removable-media defaults
// (macOS /Volumes; Linux /media, /mnt, /run/media) plus any ASTRO_BROWSE_ROOTS extras.
func (s *Server) browseRoots() []string {
	return localfs.Roots(s.cfg.BrowseRoots)
}

// localDrives lists the mounted external drives so the UI can offer "inspect an external drive and copy it
// to S3". GET /api/local/drives
func (s *Server) localDrives(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"drives": localfs.Drives(s.browseRoots())})
}

// localBrowse lists one directory level under an allowed external-drive path (confined to the browse roots,
// symlink escapes rejected). GET /api/local/browse?path=<abs>
func (s *Server) localBrowse(w http.ResponseWriter, r *http.Request) {
	listing, err := localfs.Browse(s.browseRoots(), r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, localfs.ErrForbidden):
		badRequest(w, "path is outside the allowed external-drive roots")
	case err != nil:
		serverError(w, err)
	default:
		writeJSON(w, http.StatusOK, listing)
	}
}

// localUpload enqueues a SMART copy (content-verified sync — uploads only files missing or corrupted) of an
// external-drive folder to S3, mirroring it under <prefix>/<folderName>/. The source path is re-validated
// against the browse allowlist here — the client's path is never trusted on its own. It reuses the whole
// transfer job lane + SSE progress stack, so the frontend shows live progress via the returned job id.
// POST /api/local/upload {path, bucket, prefix}
func (s *Server) localUpload(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`
		Bucket string `json:"bucket"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	abs, ok := localfs.Allowed(s.browseRoots(), body.Path)
	if !ok {
		badRequest(w, "path is outside the allowed external-drive roots")
		return
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		badRequest(w, "path is not a directory")
		return
	}
	req := job.RunRequest{
		Path: abs, // target lock + session key; NOT confined to the data dir
		Mode: "transfer",
		Transfer: &job.TransferRequest{
			Op:        "sync",
			Verify:    true,               // upload only what is missing or corrupted
			LocalRoot: filepath.Dir(abs),  // key = <prefix>/<folderName>/<fileRel>
			RelPath:   filepath.Base(abs), // the folder name
			Bucket:    body.Bucket,
			Prefix:    body.Prefix,
			Namespace: "", // external mirror: no data/ segment, no classified plan
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}
