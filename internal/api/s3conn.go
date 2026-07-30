package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
)

// This file is the UI's S3 manager: CRUD + "test connection" for encrypted connections, and a full object
// browser (buckets/objects, upload, download, delete, create folder/bucket) over any saved connection. The
// secret access key is accepted on create/update, encrypted, and NEVER returned (store.S3Connection marks
// it json:"-"). The default connection also drives the pipeline (see s3Config / s3ConfigResolved).

// connBody is the create/update payload. SecretKey is write-only; blank on update keeps the stored secret.
type connBody struct {
	Name                string `json:"name"`
	Endpoint            string `json:"endpoint"`
	Region              string `json:"region"`
	AccessKeyID         string `json:"access_key_id"`
	SecretKey           string `json:"secret_access_key"`
	UseSSL              bool   `json:"use_ssl"`
	MakeDefault         bool   `json:"make_default"`
	DefaultStorageClass string `json:"default_storage_class"` // instant class only (uploads must stay readable)
}

// s3ConnReady guards every connection endpoint on encryption being available (else the whole feature is off).
func (s *Server) s3ConnReady(w http.ResponseWriter) bool {
	if s.s3conn == nil {
		writeJSON(w, http.StatusServiceUnavailable,
			map[string]string{"error": "S3 connections require encryption — set ASTRO_ENCRYPTION_KEY"})
		return false
	}
	return true
}

func regionOrDefault(r string) string {
	if r == "" {
		return "us-east-1"
	}
	return r
}

// listConnections returns all saved connections (secret keys never included). GET /api/s3/connections
func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	conns, err := s.s3conn.List(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if conns == nil {
		conns = []store.S3Connection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": conns})
}

// createConnection saves a new connection (secret encrypted at rest). POST /api/s3/connections
func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	var b connBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if b.Name == "" || b.AccessKeyID == "" || b.SecretKey == "" {
		badRequest(w, "name, access_key_id and secret_access_key are required")
		return
	}
	id, err := s.s3conn.Create(r.Context(), b.Name, b.Endpoint, regionOrDefault(b.Region),
		b.AccessKeyID, b.SecretKey, b.UseSSL, b.MakeDefault, b.DefaultStorageClass)
	if err != nil {
		badRequest(w, err.Error()) // e.g. an archived default class is rejected
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// updateConnection edits a connection; a blank secret_access_key keeps the stored one. PUT /api/s3/connections/{id}
func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	var b connBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if b.Name == "" || b.AccessKeyID == "" {
		badRequest(w, "name and access_key_id are required")
		return
	}
	if err := s.s3conn.Update(r.Context(), id, b.Name, b.Endpoint, regionOrDefault(b.Region),
		b.AccessKeyID, b.SecretKey, b.UseSSL, b.DefaultStorageClass); err != nil {
		badRequest(w, err.Error()) // e.g. an archived default class is rejected
		return
	}
	if b.MakeDefault {
		if err := s.s3conn.SetDefault(r.Context(), id); err != nil {
			serverError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deleteConnection removes a connection. DELETE /api/s3/connections/{id}
func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.s3conn.Delete(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// setDefaultConnection makes a connection the pipeline's default. POST /api/s3/connections/{id}/default
func (s *Server) setDefaultConnection(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.s3conn.SetDefault(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// testConnection tries to connect with UNSAVED credentials from the body (the "Test connection" button in
// the add/edit form) and reports reachability + the visible buckets. POST /api/s3/connections/test
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	var b connBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	s.doTestConfig(w, r, s3store.Config{
		Endpoint: b.Endpoint, Region: regionOrDefault(b.Region),
		AccessKeyID: b.AccessKeyID, SecretKey: b.SecretKey, UseSSL: b.UseSSL,
	})
}

// testSavedConnection tests an already-saved connection (decrypting it). POST /api/s3/connections/{id}/test
func (s *Server) testSavedConnection(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	cfg, err := s.s3conn.ConfigFor(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	s.doTestConfig(w, r, cfg)
}

// doTestConfig connects and lists buckets with a short timeout, always replying 200 with {ok, buckets|error}
// so the UI can show a friendly result without treating a bad key as an HTTP error.
func (s *Server) doTestConfig(w http.ResponseWriter, r *http.Request, cfg s3store.Config) {
	if !cfg.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "access key and secret are required"})
		return
	}
	client, err := s3store.New(cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "buckets": buckets})
}

// --- object management (over a connection id in ?conn=) ---

// manageClient resolves the connection named by ?conn= into an S3 client.
func (s *Server) manageClient(w http.ResponseWriter, r *http.Request) (*s3store.Client, bool) {
	if !s.s3ConnReady(w) {
		return nil, false
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("conn"), 10, 64)
	if err != nil {
		badRequest(w, "conn (connection id) is required")
		return nil, false
	}
	client, err := s.s3conn.ClientFor(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return nil, false
	}
	return client, true
}

// manageBuckets lists a connection's buckets. GET /api/s3/manage/buckets?conn=
func (s *Server) manageBuckets(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	buckets, err := client.ListBuckets(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	if buckets == nil {
		buckets = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": buckets})
}

// manageCreateBucket creates a bucket. POST /api/s3/manage/buckets?conn=  body {name, region}
func (s *Server) manageCreateBucket(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	var b struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Name == "" {
		badRequest(w, "name is required")
		return
	}
	if err := client.MakeBucket(r.Context(), b.Name, b.Region); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// manageDeleteBucket deletes a bucket (?force=1 empties it first). DELETE /api/s3/manage/buckets?conn=&bucket=&force=
func (s *Server) manageDeleteBucket(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	if q.Get("force") == "1" {
		if err := client.RemovePrefix(r.Context(), bucket, ""); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := client.RemoveBucket(r.Context(), bucket); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// s3Object is one entry in an object listing (a sub-folder or a file). StorageClass ("" == STANDARD) lets
// the explorer badge archived objects; Archived is the derived "needs a thaw before download" flag.
type s3Object struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Size         int64  `json:"size,omitempty"`
	ModTimeMs    int64  `json:"mod_time_ms,omitempty"`
	IsDir        bool   `json:"is_dir"`
	StorageClass string `json:"storage_class,omitempty"`
	Archived     bool   `json:"archived,omitempty"`
}

// manageObjects lists one folder (immediate sub-folders + files). GET /api/s3/manage/objects?conn=&bucket=&prefix=
func (s *Server) manageObjects(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	folders, files, err := client.ListDir(r.Context(), bucket, q.Get("prefix"))
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]s3Object, 0, len(folders)+len(files))
	for _, f := range folders {
		out = append(out, s3Object{Key: f, Name: path.Base(strings.TrimSuffix(f, "/")), IsDir: true})
	}
	for _, o := range files {
		if strings.HasSuffix(o.Key, "/") {
			continue // folder marker — represented by the folders list
		}
		out = append(out, s3Object{Key: o.Key, Name: path.Base(o.Key), Size: o.Size, ModTimeMs: o.ModTime,
			StorageClass: o.StorageClass, Archived: s3store.IsArchivedClass(o.StorageClass)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"objects": out})
}

// manageCreateFolder writes a zero-byte folder marker. POST /api/s3/manage/folder?conn=&bucket=&key=
func (s *Server) manageCreateFolder(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if bucket == "" || key == "" {
		badRequest(w, "bucket and key are required")
		return
	}
	if err := client.CreateFolder(r.Context(), bucket, key); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// manageDeleteObject deletes one object, or a whole folder when key ends with "/". DELETE /api/s3/manage/object?conn=&bucket=&key=
func (s *Server) manageDeleteObject(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if bucket == "" || key == "" {
		badRequest(w, "bucket and key are required")
		return
	}
	var err error
	if strings.HasSuffix(key, "/") {
		err = client.RemovePrefix(r.Context(), bucket, key)
	} else {
		err = client.Delete(r.Context(), bucket, key)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	// Prune the ledger rows for the deleted key(s) so a classified local_rel → s3_key mapping doesn't dangle
	// (the serving fallback would otherwise resolve to a now-missing key). Non-fatal: the bytes are already
	// gone, and a stray row self-heals on the next upload.
	if s.store != nil {
		if _, derr := s.store.DeleteS3ObjectsByKeyPrefix(r.Context(), bucket, strings.TrimSuffix(key, "/")); derr != nil {
			log.Printf("s3 manage delete: prune ledger for %s/%s: %v", bucket, key, derr)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// manageMove enqueues an S3→S3 move of the selected objects/folders into a destination folder as a
// transfer-lane JOB (per object: server-side copy → s3_objects ledger rekey → delete source, so a moved file
// stays resolvable by the inspector/serving fallback and a mid-way failure never strands the ledger). Running
// it as a job — rather than synchronously in this handler — streams live progress (bytes/speed/ETA) in the
// explorer + Tasks and never blocks the request until a big folder finishes. POST /api/s3/manage/move?conn=
// body {bucket, srcs, dst}. Returns 202 {id}.
func (s *Server) manageMove(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	conn, err := strconv.ParseInt(r.URL.Query().Get("conn"), 10, 64)
	if err != nil {
		badRequest(w, "conn (connection id) is required")
		return
	}
	var body struct {
		Bucket string   `json:"bucket"`
		Srcs   []string `json:"srcs"`
		Dst    string   `json:"dst"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" || len(body.Srcs) == 0 {
		badRequest(w, "bucket and at least one src are required")
		return
	}
	// Cheap early guard (the job re-derives per source): reject an obvious folder-into-itself so the user
	// gets an immediate 400 rather than a job that fails.
	for _, src := range body.Srcs {
		if src == "" {
			continue
		}
		if newKey, isDir := s3store.MoveDest(src, body.Dst); isDir && newKey != src && strings.HasPrefix(newKey, src) {
			badRequest(w, "cannot move a folder into itself")
			return
		}
	}
	id, err := s.mgr.Enqueue(r.Context(), job.RunRequest{
		Mode: "move",
		Move: &job.MoveRequest{Conn: conn, Bucket: body.Bucket, Srcs: body.Srcs, Dst: body.Dst},
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// manageTier enqueues a storage-class change (archive / restore / restore-only) of the selected
// objects/folders as a thaw-lane JOB (per object: transition an instant source, or thaw an archived source
// then transition). Archived sources are restored first: the job parks on a thaw cadence and resumes to
// finish once readable — a durable, visitable task. Runs on the explorer's connection (Conn), NOT the
// pipeline default. POST /api/s3/manage/tier?conn=  body {bucket, srcs, target_class, restore_only, tier, days}.
// Returns 202 {id}.
func (s *Server) manageTier(w http.ResponseWriter, r *http.Request) {
	if !s.s3ConnReady(w) {
		return
	}
	conn, err := strconv.ParseInt(r.URL.Query().Get("conn"), 10, 64)
	if err != nil {
		badRequest(w, "conn (connection id) is required")
		return
	}
	var body struct {
		Bucket      string   `json:"bucket"`
		Srcs        []string `json:"srcs"`
		TargetClass string   `json:"target_class"`
		RestoreOnly bool     `json:"restore_only"`
		Tier        string   `json:"tier"`
		Days        int      `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" || len(body.Srcs) == 0 {
		badRequest(w, "bucket and at least one src are required")
		return
	}
	if !body.RestoreOnly && !s3store.ValidTargetClass(body.TargetClass) {
		badRequest(w, "target_class must be a valid storage class (e.g. STANDARD, GLACIER, DEEP_ARCHIVE, GLACIER_IR)")
		return
	}
	id, err := s.mgr.Enqueue(r.Context(), job.RunRequest{
		Mode: "tier",
		TierChange: &job.TierRequest{Conn: conn, Bucket: body.Bucket, Srcs: body.Srcs,
			TargetClass: body.TargetClass, RestoreOnly: body.RestoreOnly, Tier: body.Tier, Days: body.Days},
	})
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

// manageDownload streams an object to the browser. GET /api/s3/manage/download?conn=&bucket=&key=
func (s *Server) manageDownload(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if bucket == "" || key == "" {
		badRequest(w, "bucket and key are required")
		return
	}
	// An archived (Glacier) object can't be streamed until it is restored. Reply 409 with a machine-readable
	// archived signal so the explorer offers "Restore" instead of downloading a doomed stream. GLACIER_IR and
	// restored objects read normally.
	if rd, rerr := client.Readiness(r.Context(), bucket, key); rerr == nil && rd != s3store.Readable {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "object is archived in Glacier — restore it first",
			"archived": true, "pending": rd == s3store.Pending, "key": key})
		return
	}
	rc, size, err := client.Open(r.Context(), bucket, key)
	if err != nil {
		serverError(w, err)
		return
	}
	defer func() { _ = rc.Close() }()
	writeAttachmentHeaders(w, key, size)
	_, _ = io.Copy(w, rc)
}

// writeAttachmentHeaders sets the download headers for streaming an object to the browser. The
// Content-Disposition is built with mime.FormatMediaType, which emits the RFC 6266/2231 extended form
// (filename*=utf-8”…, percent-encoded) for non-ASCII names — a hand-built quoted string would ship raw
// UTF-8 bytes (or let a crafted name break out of the quotes).
func writeAttachmentHeaders(w http.ResponseWriter, key string, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(key)}))
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
}

// manageUpload streams the request body (the raw file) to an object. POST /api/s3/manage/upload?conn=&bucket=&key=
func (s *Server) manageUpload(w http.ResponseWriter, r *http.Request) {
	client, ok := s.manageClient(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	bucket, key := q.Get("bucket"), q.Get("key")
	if bucket == "" || key == "" {
		badRequest(w, "bucket and key are required")
		return
	}
	if err := client.PutReader(r.Context(), bucket, key, r.Body, r.ContentLength); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}
