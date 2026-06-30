// Package api exposes the engine over HTTP (stdlib net/http, Go 1.22+ routing): inspect a
// directory, browse capture folders, launch and track background jobs (with SSE progress),
// read the calibration library, and serve output images.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/elevation"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/preview"
	"github.com/verove-jordan/astronomy/internal/skyevents"
	"github.com/verove-jordan/astronomy/internal/skyplan"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// Server holds the API dependencies.
type Server struct {
	mgr            *job.Manager
	store          *store.Store
	cfg            *config.Config
	scanCache      *inspect.ScanCache
	planner        *skyplan.Planner
	events         *skyevents.Engine
	lightpollution *lightpollution.Provider
	elevation      *elevation.Provider
	darksky        *darksky.Finder
	weather        *weather.Provider
}

// New builds the API server.
func New(mgr *job.Manager, st *store.Store, cfg *config.Config) *Server {
	lp := lightpollution.New(cfg)
	elev := elevation.New(cfg)
	return &Server{
		mgr:            mgr,
		store:          st,
		cfg:            cfg,
		scanCache:      inspect.NewScanCache(),
		planner:        skyplan.New(cfg.SirilCatalogDir),
		events:         skyevents.New(cfg),
		lightpollution: lp,
		elevation:      elev,
		darksky:        darksky.New(lp, elev, cfg.DarkSkyMaxCells, cfg.HorizonCandidates),
		weather:        weather.New(cfg),
	}
}

// Handler returns the HTTP handler with routes and CORS.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/inspect", s.inspect)
	mux.HandleFunc("GET /api/browse", s.browse)
	mux.HandleFunc("GET /api/masters", s.masters)
	mux.HandleFunc("POST /api/reuse/preview", s.reusePreview)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/processed", s.processed)
	mux.HandleFunc("GET /api/file", s.serveFile)
	mux.HandleFunc("GET /api/preview", s.previewFile)
	mux.HandleFunc("GET /api/sky/targets", s.skyTargets)
	mux.HandleFunc("GET /api/sky/events", s.skyEvents)
	mux.HandleFunc("GET /api/sky/series", s.skyEventSeries)
	mux.HandleFunc("GET /api/sky/polar", s.skyPolar)
	mux.HandleFunc("GET /api/sky/align", s.skyAlign)
	mux.HandleFunc("GET /api/sky/geocode", s.geocode)
	mux.HandleFunc("GET /api/sky/lightpollution", s.lightPollution)
	mux.HandleFunc("GET /api/sky/lightpollution/tiles/{z}/{x}/{y}", s.lightPollutionTile)
	mux.HandleFunc("GET /api/sky/darksites", s.darkSites)
	mux.HandleFunc("GET /api/sky/weather", s.skyWeather)
	mux.HandleFunc("GET /api/sky/weather/grid", s.skyWeatherGrid)
	return cors(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"data_dir":    s.cfg.DataDir,
		"output_dir":  s.cfg.OutputDir,
		"library_dir": s.cfg.LibraryDir,
	})
}

func (s *Server) inspect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	roots, ok := s.resolveRoots(body.Path, body.Paths)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	inv, err := s.scanCache.ScanMany(r.Context(), roots, inspect.DefaultScanOptions())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = s.cfg.DataDir
	}
	abs, ok := s.withinData(path)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		serverError(w, err)
		return
	}
	type entry struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}
	// Directories first, then files — each group already name-sorted by os.ReadDir. Files are listed
	// (so the browser shows folder contents) but are not selectable for processing; dotfiles are hidden.
	var dirs, files []entry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		it := entry{Name: e.Name(), Path: filepath.Join(abs, e.Name()), IsDir: e.IsDir()}
		if e.IsDir() {
			dirs = append(dirs, it)
		} else {
			files = append(files, it)
		}
	}
	out := append(dirs, files...)
	if out == nil {
		out = []entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": abs, "entries": out})
}

func (s *Server) masters(w http.ResponseWriter, r *http.Request) {
	masters, err := s.store.ListMasters(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"masters": masters})
}

// reusePreview reports the prior light sessions and added integration a run on the given directory
// would fold in (the "auto-discover + confirm" data), without processing.
func (s *Server) reusePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	roots, ok := s.resolveRoots(body.Path, body.Paths)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	if !s.cfg.ReuseEnabled {
		writeJSON(w, http.StatusOK, &pipeline.ReusePreview{Object: ""})
		return
	}
	pv, err := pipeline.PreviewReuseMany(r.Context(), s.store, s.scanCache, roots, s.cfg.SirilCatalogDir, s.cfg.ReuseConeDeg)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pv)
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var req job.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if req.Live != nil && req.Live.SourceKind == "s3" {
		// A livestack S3 source uses a synthetic "s3://bucket/prefix" path as the lock/display key, not a
		// filesystem path — skip data-dir confinement (and the rewrite that would corrupt the scheme).
		if req.Live.Bucket == "" {
			badRequest(w, "s3 live source requires a bucket")
			return
		}
	} else {
		roots, ok := s.resolveRoots(req.Path, req.Paths)
		if !ok {
			badRequest(w, "path must be inside the data directory")
			return
		}
		req.Path = roots[0] // primary dir: session, target lock, run naming
		if len(roots) > 1 {
			req.Paths = roots // multi-folder selection, merged into one session
		} else {
			req.Paths = nil // single folder → unchanged single-session run
		}
	}
	if req.Mode == "" {
		req.Mode = string(mode.Deepsky)
	}
	if req.Format == "" {
		req.Format = string(mode.FormatImage)
	}
	if _, err := mode.ParseMode(req.Mode); err != nil {
		badRequest(w, err.Error())
		return
	}
	if _, err := mode.ParseFormat(req.Format); err != nil {
		badRequest(w, err.Error())
		return
	}
	id, err := s.mgr.Enqueue(r.Context(), req)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": s.mgr.Cancel(id)})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), 100)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	jb, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, jb)
}

func (s *Server) jobEvents(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		serverError(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, unsubscribe := s.mgr.Subscribe(id)
	defer unsubscribe()

	// Send a snapshot first so a late subscriber sees current state.
	if jb, err := s.store.GetJob(r.Context(), id); err == nil {
		done := isTerminal(jb.Status)
		sendEvent(w, flusher, job.Event{
			JobID: id, Status: jb.Status, Progress: jb.Progress, Step: jb.CurrentStep, Done: done,
		})
		if done {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-events:
			if !open {
				return
			}
			sendEvent(w, flusher, e)
			if e.Done {
				return
			}
		}
	}
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.within(r.URL.Query().Get("path"), s.cfg.OutputDir)
	if !ok {
		badRequest(w, "path must be inside the output directory")
		return
	}
	http.ServeFile(w, r, abs)
}

// previewFile decodes a capture file (FITS/TIFF/raw/PNG/JPEG) under the data dir into a downsampled,
// linearly-normalized 16-bit buffer the viewer stretches client-side. Streams a compact binary body
// (header + uint16 samples; see preview.Preview.Encode), not JSON.
func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	path, ok := s.withinData(r.URL.Query().Get("path"))
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	if !preview.SupportedExt(path) {
		badRequest(w, "unsupported file type for preview")
		return
	}
	maxEdge := s.cfg.PreviewMaxEdge
	if q := r.URL.Query().Get("max"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			maxEdge = n
		}
	}
	pv, err := preview.Load(r.Context(), path, maxEdge)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	_ = pv.Encode(w)
}

// runSummary is one durable run discovered on disk (see GET /api/runs).
type runSummary struct {
	Object       string   `json:"object"`
	RunID        string   `json:"run_id"`
	Dir          string   `json:"dir"`
	RunJSON      string   `json:"run_json"`
	FinalPreview string   `json:"final_preview,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Channels     []string `json:"channels,omitempty"`
	CreatedAtMs  int64    `json:"created_at_ms"`
}

// listRuns scans the output directory for run.json records so any past run can be reopened from
// disk, independent of the database (e.g. CLI runs). Files are served via /api/file.
func (s *Server) listRuns(w http.ResponseWriter, _ *http.Request) {
	outAbs, err := filepath.Abs(s.cfg.OutputDir)
	if err != nil {
		serverError(w, err)
		return
	}
	matches, _ := filepath.Glob(filepath.Join(outAbs, "*", "*", "run.json"))
	runs := make([]runSummary, 0, len(matches))
	for _, p := range matches {
		runs = append(runs, summarizeRun(p))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAtMs > runs[j].CreatedAtMs })
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func summarizeRun(runJSONPath string) runSummary {
	sum := runSummary{
		Dir:     filepath.Dir(runJSONPath),
		RunJSON: runJSONPath,
		RunID:   filepath.Base(filepath.Dir(runJSONPath)),
		Object:  filepath.Base(filepath.Dir(filepath.Dir(runJSONPath))),
	}
	if data, err := os.ReadFile(runJSONPath); err == nil {
		var rj struct {
			Object string `json:"object"`
			RunID  string `json:"run_id"`
			Final  *struct {
				Mode     string   `json:"mode"`
				Channels []string `json:"channels"`
				Outputs  []string `json:"outputs"`
			} `json:"final"`
		}
		if json.Unmarshal(data, &rj) == nil {
			if rj.Object != "" {
				sum.Object = rj.Object
			}
			if rj.RunID != "" {
				sum.RunID = rj.RunID
			}
			if rj.Final != nil {
				sum.Mode, sum.Channels = rj.Final.Mode, rj.Final.Channels
				for _, o := range rj.Final.Outputs {
					if strings.HasSuffix(o, ".png") {
						sum.FinalPreview = o
						break
					}
				}
			}
		}
	}
	if info, err := os.Stat(runJSONPath); err == nil {
		sum.CreatedAtMs = info.ModTime().UnixMilli()
	}
	return sum
}

// isTerminal reports whether a job status is final (no more events will arrive).
func isTerminal(status string) bool {
	return status == store.JobSucceeded || status == store.JobFailed || status == store.JobCancelled
}

// --- helpers ---

func (s *Server) withinData(p string) (string, bool) { return s.within(p, s.cfg.DataDir) }

// resolveRoots confines each selected capture folder to the data dir and returns the cleaned absolute
// paths. A legacy single `path` is treated as a one-element list when `paths` is empty. Returns
// ok=false (caller replies 400) when nothing is given or any path escapes the data dir.
func (s *Server) resolveRoots(path string, paths []string) ([]string, bool) {
	if len(paths) == 0 && path != "" {
		paths = []string{path}
	}
	if len(paths) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, ok := s.withinData(p)
		if !ok {
			return nil, false
		}
		out = append(out, abs)
	}
	return out, true
}

func (s *Server) within(p, root string) (string, bool) {
	if p == "" {
		return "", false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if abs == rootAbs || strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) {
		return abs, true
	}
	return "", false
}

func sendEvent(w http.ResponseWriter, f http.Flusher, e job.Event) {
	b, _ := json.Marshal(e)
	fmt.Fprintf(w, "data: %s\n\n", b)
	f.Flush()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
