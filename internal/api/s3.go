package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/s3store"
)

// browseEntry is one folder/file in a browse listing, tagged with where it lives (local disk / S3 mirror).
// Local/Remote are only meaningful when a bucket was supplied; a plain local browse leaves Remote false.
type browseEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsDir  bool   `json:"is_dir"`
	Local  bool   `json:"local,omitempty"`
	Remote bool   `json:"remote,omitempty"`
}

// mergeRemoteDirs folds the S3 mirror's sub-folders (under <prefix>/data/<relToDataDir>) into the local
// dir list: a folder present on both is tagged Remote; a folder only on S3 is appended (Local=false) so
// the user can see and download it. Returns the name-sorted union.
func (s *Server) mergeRemoteDirs(ctx context.Context, abs, bucket, userPrefix string, dirs []browseEntry) ([]browseEntry, error) {
	client, err := s3store.New(s.s3Config())
	if err != nil {
		return nil, err
	}
	dataAbs, err := filepath.Abs(s.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(dataAbs, abs)
	if err != nil {
		return nil, err
	}
	s3prefix := path.Join(userPrefix, "data", filepath.ToSlash(rel))
	folders, _, err := client.ListDir(ctx, bucket, s3prefix)
	if err != nil {
		return nil, err
	}

	byName := make(map[string]int, len(dirs))
	for i := range dirs {
		byName[dirs[i].Name] = i
	}
	for _, f := range folders {
		name := path.Base(strings.TrimSuffix(f, "/"))
		if name == "" || name == "." {
			continue
		}
		if i, ok := byName[name]; ok {
			dirs[i].Remote = true
			continue
		}
		dirs = append(dirs, browseEntry{Name: name, Path: filepath.Join(abs, name), IsDir: true, Remote: true})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	return dirs, nil
}

// s3Config builds the S3 connection config from the host environment (credentials never come from the UI).
func (s *Server) s3Config() s3store.Config {
	return s3store.Config{
		Endpoint:    s.cfg.S3Endpoint,
		Region:      s.cfg.S3Region,
		AccessKeyID: s.cfg.S3AccessKeyID,
		SecretKey:   s.cfg.S3SecretAccessKey,
		UseSSL:      s.cfg.S3UseSSL,
	}
}

// s3Status reports whether S3 is configured (env credentials present) and, if so, the reachable buckets —
// so the UI can offer S3 features and a bucket picker. GET /api/s3/status
func (s *Server) s3Status(w http.ResponseWriter, r *http.Request) {
	cfg := s.s3Config()
	resp := map[string]any{"configured": cfg.Configured(), "endpoint": s.cfg.S3Endpoint}
	if !cfg.Configured() {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	client, err := s3store.New(cfg)
	if err != nil {
		resp["reachable"] = false
		resp["error"] = err.Error()
		writeJSON(w, http.StatusOK, resp)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		resp["reachable"] = false
		resp["error"] = err.Error()
	} else {
		resp["reachable"] = true
		resp["buckets"] = buckets
	}
	writeJSON(w, http.StatusOK, resp)
}

// s3Transfer enqueues an S3 transfer job (upload/sync/download/removeLocal) over one folder. The transfer
// runs on the job manager's transfer lane and streams byte progress like any job. POST /api/s3/transfer
func (s *Server) s3Transfer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Op        string `json:"op"`
		Bucket    string `json:"bucket"`
		Prefix    string `json:"prefix"`
		Namespace string `json:"namespace"`
		RelPath   string `json:"rel_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if !validTransferOp(body.Op) {
		badRequest(w, "op must be upload, sync, download or removeLocal")
		return
	}
	ns := body.Namespace
	if ns == "" {
		ns = "data"
	}
	if ns != "data" && ns != "output" {
		badRequest(w, "namespace must be data or output")
		return
	}
	if body.Bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	rel, ok := cleanRel(body.RelPath)
	if !ok {
		badRequest(w, "invalid rel_path")
		return
	}

	root := s.cfg.DataDir
	if ns == "output" {
		root = s.cfg.OutputDir
	}
	req := job.RunRequest{
		Path: filepath.Join(root, filepath.FromSlash(rel)), // target lock only; not data-dir-confined
		Mode: "transfer",
		Transfer: &job.TransferRequest{
			Op: body.Op, Bucket: body.Bucket, Prefix: body.Prefix, Namespace: ns, RelPath: rel,
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

func validTransferOp(op string) bool {
	switch op {
	case "upload", "sync", "download", "removeLocal":
		return true
	}
	return false
}

// cleanRel normalizes a slash-relative folder path and rejects anything that escapes (leading "..").
func cleanRel(rel string) (string, bool) {
	c := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))[1:]
	if c == "" || c == "." || strings.HasPrefix(c, "..") {
		return "", false
	}
	return c, true
}
