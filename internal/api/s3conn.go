package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
)

// This file is the UI's S3 manager: CRUD + "test connection" for encrypted connections, and a full object
// browser (buckets/objects, upload, download, delete, create folder/bucket) over any saved connection. The
// secret access key is accepted on create/update, encrypted, and NEVER returned (store.S3Connection marks
// it json:"-"). The default connection also drives the pipeline (see s3Config / s3ConfigResolved).

// connBody is the create/update payload. SecretKey is write-only; blank on update keeps the stored secret.
type connBody struct {
	Name        string `json:"name"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	AccessKeyID string `json:"access_key_id"`
	SecretKey   string `json:"secret_access_key"`
	UseSSL      bool   `json:"use_ssl"`
	MakeDefault bool   `json:"make_default"`
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
		b.AccessKeyID, b.SecretKey, b.UseSSL, b.MakeDefault)
	if err != nil {
		serverError(w, err)
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
		b.AccessKeyID, b.SecretKey, b.UseSSL); err != nil {
		serverError(w, err)
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

// s3Object is one entry in an object listing (a sub-folder or a file).
type s3Object struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Size      int64  `json:"size,omitempty"`
	ModTimeMs int64  `json:"mod_time_ms,omitempty"`
	IsDir     bool   `json:"is_dir"`
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
		out = append(out, s3Object{Key: o.Key, Name: path.Base(o.Key), Size: o.Size, ModTimeMs: o.ModTime})
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	rc, size, err := client.Open(r.Context(), bucket, key)
	if err != nil {
		serverError(w, err)
		return
	}
	defer func() { _ = rc.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(key)+`"`)
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = io.Copy(w, rc)
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
