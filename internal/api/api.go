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
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Server holds the API dependencies.
type Server struct {
	mgr   *job.Manager
	store *store.Store
	cfg   *config.Config
}

// New builds the API server.
func New(mgr *job.Manager, st *store.Store, cfg *config.Config) *Server {
	return &Server{mgr: mgr, store: st, cfg: cfg}
}

// Handler returns the HTTP handler with routes and CORS.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/inspect", s.inspect)
	mux.HandleFunc("GET /api/browse", s.browse)
	mux.HandleFunc("GET /api/masters", s.masters)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("GET /api/file", s.serveFile)
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
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	path, ok := s.withinData(body.Path)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	inv, err := inspect.Scan(r.Context(), path)
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
	var dirs []entry
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, entry{Name: e.Name(), Path: filepath.Join(abs, e.Name()), IsDir: true})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": abs, "entries": dirs})
}

func (s *Server) masters(w http.ResponseWriter, r *http.Request) {
	masters, err := s.store.ListMasters(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"masters": masters})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	path, ok := s.withinData(body.Path)
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	kind := body.Kind
	if kind == "" {
		kind = "process"
	}
	id, err := s.mgr.Enqueue(r.Context(), kind, path)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
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
		sendEvent(w, flusher, job.Event{
			JobID: id, Status: jb.Status, Progress: jb.Progress, Step: jb.CurrentStep,
			Done: jb.Status == store.JobSucceeded || jb.Status == store.JobFailed,
		})
		if jb.Status == store.JobSucceeded || jb.Status == store.JobFailed {
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

// --- helpers ---

func (s *Server) withinData(p string) (string, bool) { return s.within(p, s.cfg.DataDir) }

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
