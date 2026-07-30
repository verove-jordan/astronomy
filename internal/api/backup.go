package api

import (
	"encoding/json"
	"net/http"
	"path"
	"time"

	"github.com/verove-jordan/astronomy/internal/backup"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/s3store"
)

// createBackup enqueues a backup-everything job — the Postgres database, calibration library, light-
// pollution atlas and the browser app-state (favorites/setups/prefs + AI chats, exported UI-side and posted
// here) → <prefix>/backup/<stamp>/ on S3. Credentials come from the env, never the body. POST /api/backup
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bucket       string   `json:"bucket"`
		Prefix       string   `json:"prefix"`
		Components   []string `json:"components"`
		AppState     string   `json:"appstate"`
		StorageClass string   `json:"storage_class"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	if body.StorageClass != "" && !s3store.ValidTargetClass(body.StorageClass) {
		badRequest(w, "storage_class must be a valid storage class")
		return
	}
	if !s.s3Config(r.Context()).Configured() {
		badRequest(w, "S3 is not configured")
		return
	}
	req := job.RunRequest{
		Path: "backup", // synthetic lock/display key; not data-dir-confined
		Mode: "backup",
		Backup: &job.BackupRequest{
			Bucket: body.Bucket, Prefix: body.Prefix, Components: body.Components,
			AppState: body.AppState, StampMs: time.Now().UnixMilli(), StorageClass: body.StorageClass,
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// listBackups returns the manifests of every backup under <prefix>/backup/, newest first. Honors ?conn=
// (s3ConfigForRequest) so the listing reads the connection the bucket was chosen under. GET /api/backup
func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket := q.Get("bucket")
	cfg, cfgErr := s.s3ConfigForRequest(r)
	if bucket == "" || cfgErr != nil || !cfg.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"backups": []backup.Manifest{}})
		return
	}
	client, err := s3store.New(cfg)
	if err != nil {
		serverError(w, err)
		return
	}
	mans, err := backup.List(r.Context(), client, bucket, q.Get("prefix"))
	if err != nil {
		serverError(w, err)
		return
	}
	if mans == nil {
		mans = []backup.Manifest{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": mans})
}

// restoreBackup enqueues a restore job for the chosen components of one backup. The appstate component is
// applied browser-side (GET /api/backup/appstate), not by this job. POST /api/backup/restore
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bucket     string   `json:"bucket"`
		Prefix     string   `json:"prefix"`
		Stamp      string   `json:"stamp"`
		Components []string `json:"components"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" || body.Stamp == "" {
		badRequest(w, "bucket and stamp are required")
		return
	}
	if !s.s3Config(r.Context()).Configured() {
		badRequest(w, "S3 is not configured")
		return
	}
	req := job.RunRequest{
		Path: "restore", // synthetic lock/display key
		Mode: "restore",
		Restore: &job.RestoreRequest{
			Bucket: body.Bucket, Prefix: body.Prefix, Stamp: body.Stamp, Components: body.Components,
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// backupAppState returns the browser app-state JSON saved in one backup, so the UI can re-import it — this
// is how the appstate component is restored (localStorage + the AI-chat IndexedDB live only in the browser).
// GET /api/backup/appstate
func (s *Server) backupAppState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket, stamp := q.Get("bucket"), q.Get("stamp")
	cfg, cfgErr := s.s3ConfigForRequest(r) // honors ?conn= like listBackups
	if bucket == "" || stamp == "" || cfgErr != nil || !cfg.Configured() {
		badRequest(w, "bucket and stamp are required")
		return
	}
	client, err := s3store.New(cfg)
	if err != nil {
		serverError(w, err)
		return
	}
	keyPrefix := path.Join(q.Get("prefix"), "backup", stamp)
	data, err := backup.AppState(r.Context(), client, bucket, keyPrefix)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
