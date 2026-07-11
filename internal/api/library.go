package api

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/libmirror"
	"github.com/verove-jordan/astronomy/internal/store"
)

// libraryS3Sync mirrors the calibration-master library (camera darks/flats/bias/offsets + phone/DSLR ISO
// masters) to <prefix>/library/ on S3 as a content-verified per-file sync — uploading only masters that are
// missing or changed on S3, and KEEPING the local copies (the library stays a synced mirror; a later run
// transparently pulls back any matched master it happens to be missing). The multi-GB Gaia catalogues
// subtree under LibraryDir is excluded. It reuses the whole transfer job lane, so the frontend shows live
// progress via the returned job id. Credentials come from the resolved default connection / env, never the
// body. POST /api/library/s3-sync {bucket, prefix}
func (s *Server) libraryS3Sync(w http.ResponseWriter, r *http.Request) {
	var body struct {
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
	if !s.s3Config(r.Context()).Configured() {
		badRequest(w, "S3 is not configured")
		return
	}
	libDir, err := filepath.Abs(s.cfg.LibraryDir)
	if err != nil {
		serverError(w, err)
		return
	}
	req := job.RunRequest{
		Path: libDir, // target lock + session key; NOT confined to the data dir (like the external-drive copy)
		Mode: "transfer",
		Transfer: &job.TransferRequest{
			Op:          "sync",
			Verify:      true,                             // upload only masters missing or corrupted on S3
			LocalRoot:   libDir,                           // walk the library dir directly…
			RelPath:     "",                               // …RelPath empty + Namespace "library" → key = <prefix>/library/<file>
			Namespace:   libmirror.LibraryRoot,            //
			ExcludeDirs: []string{libmirror.CatalogueDir}, // never mirror the Gaia catalogues tree
			Bucket:      body.Bucket,
			Prefix:      body.Prefix,
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	// Record where the library now lives on S3 so any later run can pull a matched master back from
	// <prefix>/library/ when it is absent locally. Best-effort: a failure here doesn't fail the sync.
	if err := s.store.SetLibraryMirror(r.Context(), store.LibraryMirrorLocation{Bucket: body.Bucket, Prefix: body.Prefix}); err != nil {
		log.Printf("library s3-sync: could not record mirror location: %v", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}
