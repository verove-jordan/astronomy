package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
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
	client, err := s3store.New(s.s3Config(ctx))
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

// s3Config resolves the S3 config the pipeline uses: the UI-selected **default connection** (decrypted)
// when one is set, else the host-environment credentials (backward compatible). This is the single
// chokepoint that lets a UI-managed connection drive import/process/results/backup.
func (s *Server) s3Config(ctx context.Context) s3store.Config {
	if s.s3conn != nil {
		if cfg, ok, err := s.s3conn.DefaultConfig(ctx); err == nil && ok {
			return cfg
		}
	}
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
	cfg := s.s3Config(r.Context())
	resp := map[string]any{"configured": cfg.Configured(), "endpoint": cfg.Endpoint}
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
// s3BrowseEntry is one folder/file when browsing the real S3 bucket (the Import "S3 Storage" tab). Path
// is the sub-path relative to the configured prefix, so the frontend navigates by rel and can derive the
// local landing dir (<DataDir>/<rel>) for a download.
type s3BrowseEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	IsDir  bool   `json:"is_dir"`
	Remote bool   `json:"remote"`
}

// s3Browse lists one level of the real bucket at <prefix>/<rel> using the default (pipeline) S3
// connection — unlike /api/s3/manage/objects it is not connection-scoped. Feeds the Import "S3 Storage"
// tab so a user can pick their own bucket folders, which need not follow the AstroStack <prefix>/data
// mirror. GET /api/s3/browse?bucket=&prefix=&rel=
func (s *Server) s3Browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	cfg := s.s3Config(r.Context())
	if !cfg.Configured() {
		badRequest(w, "S3 is not configured")
		return
	}
	client, err := s3store.New(cfg)
	if err != nil {
		serverError(w, err)
		return
	}
	rel, _ := cleanRel(q.Get("rel")) // "" (prefix root) when empty/invalid; cleanRel drops any ".." escape
	folders, files, err := client.ListDir(r.Context(), bucket, path.Join(q.Get("prefix"), rel))
	if err != nil {
		serverError(w, err)
		return
	}
	entries := make([]s3BrowseEntry, 0, len(folders)+len(files))
	for _, f := range folders {
		name := path.Base(strings.TrimSuffix(f, "/"))
		entries = append(entries, s3BrowseEntry{Name: name, Path: path.Join(rel, name), IsDir: true, Remote: true})
	}
	for _, o := range files {
		if strings.HasSuffix(o.Key, "/") {
			continue // folder marker — already in folders
		}
		name := path.Base(o.Key)
		entries = append(entries, s3BrowseEntry{Name: name, Path: path.Join(rel, name), IsDir: false, Remote: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"rel": rel, "entries": entries})
}

// s3Import downloads a real S3 folder (<prefix>/<rel>) into <DataDir>/<rel> so it can be inspected and run
// like a normal local capture — the Import "S3 Storage" tab calls it before inspecting S3-picked folders.
// It enqueues an ordinary download transfer with an EMPTY namespace, which the transfer layer resolves to
// KeyPrefix=<prefix> and LocalRoot=DataDir (no AstroStack data/ mirror). POST /api/s3/import
// {bucket, prefix, rel}
func (s *Server) s3Import(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bucket string `json:"bucket"`
		Prefix string `json:"prefix"`
		Rel    string `json:"rel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	rel, ok := cleanRel(body.Rel)
	if !ok {
		badRequest(w, "invalid rel")
		return
	}
	req := job.RunRequest{
		Path: filepath.Join(s.cfg.DataDir, filepath.FromSlash(rel)), // target lock; lands under DataDir
		Mode: "transfer",
		Transfer: &job.TransferRequest{
			Op: "download", Bucket: body.Bucket, Prefix: body.Prefix, Namespace: "", RelPath: rel,
		},
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

func cleanRel(rel string) (string, bool) {
	c := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))[1:]
	if c == "" || c == "." || strings.HasPrefix(c, "..") {
		return "", false
	}
	return c, true
}

// --- S3-fallback serving (previews & results after the local copy was freed) ---

// ensureServable resolves a local path to serve. If abs (already confined to root) exists on disk it is
// returned as-is. Otherwise, when S3 is configured and the request names a bucket, the mirror object
// (<prefix>/<namespace>/<relToRoot>) is downloaded once into the regenerable serve cache
// (WorkDir/cache/s3/<namespace>/<rel>) and that path is returned — so previews and results keep loading
// after "Free local" moved them to S3. ok=false means neither local nor fetchable (caller replies 404).
func (s *Server) ensureServable(ctx context.Context, r *http.Request, abs, root, namespace string) (string, bool) {
	if _, err := os.Stat(abs); err == nil {
		return abs, true
	}
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" || !s.s3Config(ctx).Configured() {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	relSlash := filepath.ToSlash(rel)
	cachePath := filepath.Join(s.cfg.WorkDir, "cache", "s3", namespace, filepath.FromSlash(relSlash))
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, true // already fetched this file into the cache
	}
	client, err := s3store.New(s.s3Config(ctx))
	if err != nil {
		return "", false
	}
	key := path.Join(q.Get("prefix"), namespace, relSlash)
	if err := client.Download(ctx, bucket, key, cachePath, nil); err != nil {
		return "", false
	}
	return cachePath, true
}

// s3OutputRuns lists run.json objects under <prefix>/output/ and maps each to the absolute local path it
// mirrors, so listRuns can surface runs whose local output tree was freed. s3Key is set on every ref (they
// live only on S3). Returns nil on any error or when nothing matches (soft-fail to local-only).
func (s *Server) s3OutputRuns(ctx context.Context, bucket, userPrefix, outAbs string) []runFileRef {
	client, err := s3store.New(s.s3Config(ctx))
	if err != nil {
		return nil
	}
	outPrefix := path.Join(userPrefix, "output") + "/"
	objs, err := client.List(ctx, bucket, outPrefix)
	if err != nil {
		return nil
	}
	var refs []runFileRef
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, "/run.json") {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(o.Key, outPrefix), "/run.json")
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		refs = append(refs, runFileRef{
			path:  filepath.Join(outAbs, filepath.FromSlash(rel), "run.json"),
			mtime: o.ModTime,
			s3Key: o.Key,
		})
	}
	return refs
}

// summarizeS3Run reads one run.json straight from the S3 mirror (its local copy was freed) and summarizes
// it against the local path it mirrors, so the run's output previews resolve through the same
// local-first/S3-fallback serving. Falls back to a bare summary if the object can't be read.
func (s *Server) summarizeS3Run(ctx context.Context, bucket, key, localRunJSON string, mtimeMs int64) runSummary {
	client, err := s3store.New(s.s3Config(ctx))
	if err != nil {
		return summarizeRunBytes(nil, localRunJSON, mtimeMs)
	}
	data, err := client.GetBytes(ctx, bucket, key)
	if err != nil {
		return summarizeRunBytes(nil, localRunJSON, mtimeMs)
	}
	return summarizeRunBytes(data, localRunJSON, mtimeMs)
}

// remoteDataDirExists reports whether a capture folder (absolute, under DataDir) has any objects on the S3
// mirror (<prefix>/data/<rel>). Used by /api/processed so a folder freed after an S3 push is not shown as
// deleted. Any error (misconfig, unreachable) soft-fails to false.
func (s *Server) remoteDataDirExists(ctx context.Context, client *s3store.Client, bucket, userPrefix, localAbs string) bool {
	if client == nil {
		return false
	}
	dataAbs, err := filepath.Abs(s.cfg.DataDir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dataAbs, localAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	prefix := path.Join(userPrefix, "data", filepath.ToSlash(rel))
	folders, files, err := client.ListDir(ctx, bucket, prefix)
	if err != nil {
		return false
	}
	return len(folders) > 0 || len(files) > 0
}
