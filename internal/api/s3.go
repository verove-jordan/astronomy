package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

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
// the user can see and download it. Returns the name-sorted union. cfg is the request-resolved S3 config
// (see s3ConfigForRequest) so the listing targets the connection the bucket was chosen under.
func (s *Server) mergeRemoteDirs(ctx context.Context, cfg s3store.Config, abs, bucket, userPrefix string, dirs []browseEntry, fresh bool) ([]browseEntry, error) {
	dataAbs, err := filepath.Abs(s.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(dataAbs, abs)
	if err != nil {
		return nil, err
	}
	relSlash := filepath.ToSlash(rel)
	folders, _, err := s.listDirCached(ctx, cfg, bucket, path.Join(userPrefix, "data", relSlash), fresh)
	if err != nil {
		return nil, err
	}
	// Union the legacy-mirror child folders with the classified-layout child folders the DB ledger knows
	// (a classified capture has no data/<rel>/ folder to list), so the browser shows both layouts.
	names := childDirNames(folders)
	if s.store != nil {
		if kids, err := s.store.ListS3ChildDirs(ctx, bucket, userPrefix, relSlash); err == nil {
			names = append(names, kids...)
		}
	}

	byName := make(map[string]int, len(dirs))
	for i := range dirs {
		byName[dirs[i].Name] = i
	}
	for _, name := range names {
		if name == "" || name == "." {
			continue
		}
		if i, ok := byName[name]; ok {
			dirs[i].Remote = true
			continue
		}
		byName[name] = len(dirs)
		dirs = append(dirs, browseEntry{Name: name, Path: filepath.Join(abs, name), IsDir: true, Remote: true})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	return dirs, nil
}

// wantsFresh reports whether the request asks to bypass the short-TTL listing cache (the Refresh
// button appends ?fresh=1) so an explicit refresh always re-lists live.
func wantsFresh(r *http.Request) bool {
	v := r.URL.Query().Get("fresh")
	return v == "1" || v == "true"
}

// childDirNames extracts the base folder names from S3 ListDir "folder/" entries.
func childDirNames(folders []string) []string {
	out := make([]string, 0, len(folders))
	for _, f := range folders {
		out = append(out, path.Base(strings.TrimSuffix(f, "/")))
	}
	return out
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

// s3ConfigForRequest resolves the S3 config for one HTTP request. When the request names a saved
// connection (?conn=<id> — the frontend tags it alongside bucket/prefix so the pair can never drift
// apart), that connection's config is used; a present-but-unresolvable id is an error rather than
// silently serving another connection's bucket. Without a valid numeric conn param it falls back to
// s3Config (default connection → env), so pre-existing URLs behave exactly as before.
func (s *Server) s3ConfigForRequest(r *http.Request) (s3store.Config, error) {
	raw := r.URL.Query().Get("conn")
	if raw == "" || s.s3conn == nil {
		return s.s3Config(r.Context()), nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return s.s3Config(r.Context()), nil // not a connection id — behave as before
	}
	cfg, err := s.s3conn.ConfigFor(r.Context(), id)
	if err != nil {
		return s3store.Config{}, fmt.Errorf("resolve s3 connection %d: %w", id, err)
	}
	return cfg, nil
}

// s3Status reports whether S3 is configured (env credentials present) and, if so, the reachable buckets —
// so the UI can offer S3 features and a bucket picker. conn_id names the default connection (when one is
// set) so the frontend can persist it next to the bucket/prefix it picks. GET /api/s3/status
func (s *Server) s3Status(w http.ResponseWriter, r *http.Request) {
	cfg := s.s3Config(r.Context())
	resp := map[string]any{"configured": cfg.Configured(), "endpoint": cfg.Endpoint}
	if s.s3conn != nil {
		if id, ok, err := s.s3conn.DefaultID(r.Context()); err == nil && ok {
			resp["conn_id"] = id
		}
	}
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

// s3Browse lists one level of the real bucket at <prefix>/<rel> using the request's S3 connection
// (?conn=, default connection otherwise) — unlike /api/s3/manage/objects the conn tag is optional. Feeds
// the Import "S3 Storage" tab so a user can pick their own bucket folders, which need not follow the
// AstroStack <prefix>/data mirror. GET /api/s3/browse?bucket=&prefix=&rel=&conn=
func (s *Server) s3Browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket := q.Get("bucket")
	if bucket == "" {
		badRequest(w, "bucket is required")
		return
	}
	cfg, err := s.s3ConfigForRequest(r)
	if err != nil {
		serverError(w, err)
		return
	}
	if !cfg.Configured() {
		badRequest(w, "S3 is not configured")
		return
	}
	rel, _ := cleanRel(q.Get("rel")) // "" (prefix root) when empty/invalid; cleanRel drops any ".." escape
	folders, files, err := s.listDirCached(r.Context(), cfg, bucket, path.Join(q.Get("prefix"), rel), wantsFresh(r))
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
	if bucket == "" {
		return "", false
	}
	cfg, cfgErr := s.s3ConfigForRequest(r) // honor ?conn= so the fallback reads the bucket's own connection
	if cfgErr != nil || !cfg.Configured() {
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
	client, err := s3store.New(cfg)
	if err != nil {
		return "", false
	}
	key := s.mirrorKey(ctx, bucket, q.Get("prefix"), namespace, relSlash)
	if err := client.Download(ctx, bucket, key, cachePath, nil); err != nil {
		return "", false
	}
	return cachePath, true
}

// mirrorKey resolves the S3 key of a mirrored file. For the classified data namespace it prefers the
// persisted local-rel → key mapping (a classified key is not derivable from the path), falling back to the
// legacy <prefix>/<namespace>/<rel> mirror key; output/backup are always legacy. relSlash is the file's
// DataDir/OutputDir-relative slash path.
func (s *Server) mirrorKey(ctx context.Context, bucket, userPrefix, namespace, relSlash string) string {
	if namespace == "data" && s.store != nil {
		if o, ok, err := s.store.GetS3Object(ctx, bucket, userPrefix, relSlash); err == nil && ok {
			return o.S3Key
		}
	}
	return path.Join(userPrefix, namespace, relSlash)
}

// s3OutputRuns lists run.json objects under <prefix>/output/ and maps each to the absolute local path it
// mirrors, so listRuns can surface runs whose local output tree was freed. s3Key is set on every ref (they
// live only on S3). cfg is the request-resolved S3 config (s3ConfigForRequest). Returns nil on any error
// or when nothing matches (soft-fail to local-only).
func (s *Server) s3OutputRuns(ctx context.Context, cfg s3store.Config, bucket, userPrefix, outAbs string) []runFileRef {
	client, err := s3store.New(cfg)
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
// local-first/S3-fallback serving. cfg is the request-resolved S3 config (s3ConfigForRequest). Falls back
// to a bare summary if the object can't be read.
func (s *Server) summarizeS3Run(ctx context.Context, cfg s3store.Config, bucket, key, localRunJSON string, mtimeMs int64) runSummary {
	client, err := s3store.New(cfg)
	if err != nil {
		return summarizeRunBytes(nil, localRunJSON, mtimeMs)
	}
	data, err := client.GetBytes(ctx, bucket, key)
	if err != nil {
		return summarizeRunBytes(nil, localRunJSON, mtimeMs)
	}
	return summarizeRunBytes(data, localRunJSON, mtimeMs)
}

// remoteDirsExist reports, for each DataDir-relative folder rel, whether it still exists on the S3 mirror —
// classified ledger or legacy <prefix>/data/<rel>. It batches by parent directory so N locally-freed folders
// cost at most one listing per unique parent (usually one per capture target), reusing the warm-client
// listing cache. Used by /api/processed so a folder freed after an S3 push is not shown as deleted; any
// per-parent error soft-falls to that parent's ledger-only answer.
func (s *Server) remoteDirsExist(ctx context.Context, cfg s3store.Config, bucket, userPrefix string, rels []string) map[string]bool {
	parents := make([]string, 0)
	byParent := make(map[string][]string)
	relParent := make(map[string]string, len(rels))
	relBase := make(map[string]string, len(rels))
	for _, rel := range rels {
		parent := path.Dir(rel)
		if parent == "." {
			parent = ""
		}
		base := path.Base(rel)
		if _, seen := byParent[parent]; !seen {
			parents = append(parents, parent)
		}
		byParent[parent] = append(byParent[parent], base)
		relParent[rel] = parent
		relBase[rel] = base
	}

	// Each parent is independent → resolve concurrently into its own slot (no shared-map race).
	sets := make([]map[string]bool, len(parents))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, parent := range parents {
		i, parent := i, parent
		g.Go(func() error {
			sets[i] = s.mirrorChildren(gctx, cfg, bucket, userPrefix, parent, byParent[parent])
			return nil
		})
	}
	_ = g.Wait()

	byParentSet := make(map[string]map[string]bool, len(parents))
	for i, parent := range parents {
		byParentSet[parent] = sets[i]
	}
	out := make(map[string]bool, len(rels))
	for _, rel := range rels {
		if set := byParentSet[relParent[rel]]; set != nil {
			out[rel] = set[relBase[rel]]
		}
	}
	return out
}

// mirrorChildren returns the set of child folder names present under <prefix>/data/<parent> on the mirror:
// the DB ledger's children (classified layout), plus — only when some wanted base isn't ledger-covered — one
// live S3 listing (legacy mirror). Preserving the ledger-first early-out keeps a fully-classified capture at
// zero S3 round-trips, exactly like the old per-folder HasS3ObjectsUnder short-circuit.
func (s *Server) mirrorChildren(ctx context.Context, cfg s3store.Config, bucket, userPrefix, parent string, want []string) map[string]bool {
	set := make(map[string]bool)
	if s.store != nil {
		if kids, err := s.store.ListS3ChildDirs(ctx, bucket, userPrefix, parent); err == nil {
			for _, k := range kids {
				set[k] = true
			}
		}
	}
	covered := true
	for _, b := range want {
		if !set[b] {
			covered = false
			break
		}
	}
	if covered {
		return set
	}
	folders, _, err := s.listDirCached(ctx, cfg, bucket, path.Join(userPrefix, "data", parent), false)
	if err != nil {
		return set
	}
	for _, name := range childDirNames(folders) {
		set[name] = true
	}
	return set
}
