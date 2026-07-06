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

	"github.com/verove-jordan/astronomy/internal/agent"
	"github.com/verove-jordan/astronomy/internal/canopy"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/elevation"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/job"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/preview"
	"github.com/verove-jordan/astronomy/internal/routing"
	"github.com/verove-jordan/astronomy/internal/s3conn"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/secret"
	"github.com/verove-jordan/astronomy/internal/skyevents"
	"github.com/verove-jordan/astronomy/internal/skyplan"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/thumb"
	"github.com/verove-jordan/astronomy/internal/toolhealth"
	"github.com/verove-jordan/astronomy/internal/turns"
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
	canopy         *canopy.Provider
	darksky        *darksky.Finder
	weather        *weather.Provider
	s3conn         *s3conn.Service     // UI-managed S3 connections; nil when encryption is unavailable
	agent          *agent.Runner       // tool-using AstroAgent (drives the local model over the app's tools)
	agentTurns     *turns.Sessions     // live turns (agent chat + supervised-job conversations), streamed over SSE
	toolHealth     *toolhealth.Checker // environment health (tool deep probes + catalogue presence)
}

// New builds the API server. hub is the shared turn transport (also handed to the job manager) so a
// supervised finish and the AstroAgent chat stream over one SSE mechanism.
func New(mgr *job.Manager, st *store.Store, cfg *config.Config, hub *turns.Sessions) *Server {
	lp := lightpollution.New(cfg)
	cp := canopy.New(cfg)
	elev := elevation.New(cfg, cp)
	rt := routing.New(cfg)
	dk := darksky.New(lp, elev, cfg.DarkSkyMaxCells, cfg.HorizonCandidates,
		darksky.WithScore(darksky.ScoreConfig{
			DarkWeight:       cfg.DarkSkyDarkWeight,
			SouthWeight:      cfg.DarkSkySouthWeight,
			MaxSouthBlockDeg: cfg.DarkSkyMaxSouthBlockDeg,
		}),
		darksky.WithRouter(rt))
	s := &Server{
		mgr:            mgr,
		store:          st,
		cfg:            cfg,
		scanCache:      inspect.NewScanCache(),
		planner:        skyplan.New(cfg.SirilCatalogDir),
		events:         skyevents.New(cfg),
		lightpollution: lp,
		elevation:      elev,
		canopy:         cp,
		darksky:        dk,
		weather:        weather.New(cfg),
		s3conn:         newS3ConnService(st, cfg),
		agentTurns:     hub,
		toolHealth:     toolhealth.New(cfg),
	}
	s.buildAgent()
	return s
}

// newS3ConnService builds the encrypted-connection service, or returns nil (the feature is disabled and env
// S3 still works) when the master key can't be resolved — logged once, never fatal.
func newS3ConnService(st *store.Store, cfg *config.Config) *s3conn.Service {
	box, err := secret.NewBox(cfg.EncryptionKey, cfg.SecretKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: S3 connection encryption unavailable (%v) — set ASTRO_ENCRYPTION_KEY\n", err)
		return nil
	}
	return s3conn.New(st, box)
}

// Handler returns the HTTP handler with routes and CORS.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/environment", s.environment)
	mux.HandleFunc("POST /api/inspect", s.inspect)
	mux.HandleFunc("GET /api/browse", s.browse)
	mux.HandleFunc("GET /api/masters", s.masters)
	mux.HandleFunc("GET /api/phone-masters", s.phoneMasters)
	mux.HandleFunc("POST /api/reuse/preview", s.reusePreview)
	mux.HandleFunc("POST /api/calib/preview", s.calibPreview)
	mux.HandleFunc("POST /api/jobs", s.createJob)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/restart", s.restartJob)
	mux.HandleFunc("POST /api/jobs/{id}/refine", s.refineJob)
	mux.HandleFunc("GET /api/jobs/{id}/iterations", s.jobIterations)
	mux.HandleFunc("GET /api/jobs/{id}/events", s.jobEvents)
	mux.HandleFunc("POST /api/series", s.createSeries)
	mux.HandleFunc("GET /api/series", s.listSeries)
	mux.HandleFunc("GET /api/series/{id}", s.getSeries)
	mux.HandleFunc("POST /api/series/{id}/continue", s.setSeriesStatus("active"))
	mux.HandleFunc("POST /api/series/{id}/stop", s.setSeriesStatus("stopped"))
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/processed", s.processed)
	mux.HandleFunc("GET /api/file", s.serveFile)
	mux.HandleFunc("GET /api/preview", s.previewFile)
	mux.HandleFunc("GET /api/thumb", s.serveThumb)
	mux.HandleFunc("GET /api/sky/targets", s.skyTargets)
	mux.HandleFunc("GET /api/sky/events", s.skyEvents)
	mux.HandleFunc("GET /api/sky/series", s.skyEventSeries)
	mux.HandleFunc("GET /api/sky/polar", s.skyPolar)
	mux.HandleFunc("GET /api/sky/align", s.skyAlign)
	mux.HandleFunc("GET /api/sky/geocode", s.geocode)
	mux.HandleFunc("GET /api/s3/status", s.s3Status)
	mux.HandleFunc("POST /api/s3/transfer", s.s3Transfer)
	mux.HandleFunc("GET /api/s3/browse", s.s3Browse)
	mux.HandleFunc("POST /api/s3/import", s.s3Import)
	mux.HandleFunc("GET /api/s3/connections", s.listConnections)
	mux.HandleFunc("POST /api/s3/connections", s.createConnection)
	mux.HandleFunc("POST /api/s3/connections/test", s.testConnection)
	mux.HandleFunc("PUT /api/s3/connections/{id}", s.updateConnection)
	mux.HandleFunc("DELETE /api/s3/connections/{id}", s.deleteConnection)
	mux.HandleFunc("POST /api/s3/connections/{id}/default", s.setDefaultConnection)
	mux.HandleFunc("POST /api/s3/connections/{id}/test", s.testSavedConnection)
	mux.HandleFunc("GET /api/s3/manage/buckets", s.manageBuckets)
	mux.HandleFunc("POST /api/s3/manage/buckets", s.manageCreateBucket)
	mux.HandleFunc("DELETE /api/s3/manage/buckets", s.manageDeleteBucket)
	mux.HandleFunc("GET /api/s3/manage/objects", s.manageObjects)
	mux.HandleFunc("POST /api/s3/manage/folder", s.manageCreateFolder)
	mux.HandleFunc("DELETE /api/s3/manage/object", s.manageDeleteObject)
	mux.HandleFunc("GET /api/s3/manage/download", s.manageDownload)
	mux.HandleFunc("POST /api/s3/manage/upload", s.manageUpload)
	mux.HandleFunc("POST /api/backup", s.createBackup)
	mux.HandleFunc("GET /api/backup", s.listBackups)
	mux.HandleFunc("POST /api/backup/restore", s.restoreBackup)
	mux.HandleFunc("GET /api/backup/appstate", s.backupAppState)
	mux.HandleFunc("GET /api/sky/lightpollution", s.lightPollution)
	mux.HandleFunc("GET /api/sky/lightpollution/atlas", s.atlasStatus)
	mux.HandleFunc("POST /api/sky/lightpollution/atlas", s.buildAtlas)
	mux.HandleFunc("GET /api/sky/lightpollution/tiles/{z}/{x}/{y}", s.lightPollutionTile)
	mux.HandleFunc("GET /api/sky/darksites", s.darkSites)
	mux.HandleFunc("GET /api/sky/canopy/atlas", s.canopyAtlasStatus)
	mux.HandleFunc("POST /api/sky/canopy/atlas", s.canopyBuildAtlas)
	mux.HandleFunc("GET /api/sky/weather", s.skyWeather)
	mux.HandleFunc("GET /api/sky/weather/grid", s.skyWeatherGrid)
	mux.HandleFunc("GET /api/agent/status", s.agentStatus)
	mux.HandleFunc("POST /api/agent/chat", s.agentChat)
	mux.HandleFunc("GET /api/agent/turns/{id}/events", s.agentTurnEvents)
	mux.HandleFunc("POST /api/agent/turns/{id}/confirm", s.agentTurnConfirm)
	mux.HandleFunc("POST /api/agent/turns/{id}/message", s.agentTurnMessage)
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

// environment reports whether every external tool the pipeline drives can ACTUALLY run (deep
// probes, not binary lookups) plus the offline plate-solve catalogue situation — so the UI can warn
// before a run instead of the user diagnosing a silently-degraded image afterwards.
func (s *Server) environment(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.toolHealth.Report(r.Context()))
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
	q := r.URL.Query()
	path := q.Get("path")
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
	// Directories first, then files — each group already name-sorted by os.ReadDir. Files are listed
	// (so the browser shows folder contents) but are not selectable for processing; dotfiles are hidden.
	var dirs, files []browseEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		it := browseEntry{Name: e.Name(), Path: filepath.Join(abs, e.Name()), IsDir: e.IsDir(), Local: true}
		if e.IsDir() {
			dirs = append(dirs, it)
		} else {
			files = append(files, it)
		}
	}
	// When a bucket is supplied and S3 is configured, fold in the mirror's folders so the browser can
	// show local / cloud / both presence (and surface S3-only folders to download). Soft-fails to local.
	// The config honors ?conn= so the mirror listing targets the connection the bucket was chosen under.
	if bucket := q.Get("bucket"); bucket != "" {
		if cfg, err := s.s3ConfigForRequest(r); err == nil && cfg.Configured() {
			if merged, err := s.mergeRemoteDirs(r.Context(), cfg, abs, bucket, q.Get("prefix"), dirs); err == nil {
				dirs = merged
			}
		}
	}
	out := append(dirs, files...)
	if out == nil {
		out = []browseEntry{}
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

// phoneMasters returns the reusable phone/DSLR calibration masters (iPhone DNG darks/bias/flats) built
// by the milkyway path, keyed by ISO/exposure/dimensions.
func (s *Server) phoneMasters(w http.ResponseWriter, r *http.Request) {
	masters, err := s.store.ListPhoneMasters(r.Context())
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

// calibPreview reports which library master dark/flat/bias would calibrate each inspected light channel,
// so the Import "Calibration" panel can show + let the user exclude them. POST /api/calib/preview
func (s *Server) calibPreview(w http.ResponseWriter, r *http.Request) {
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
	pv, err := pipeline.PreviewCalibration(r.Context(), s.scanCache, s.store, roots)
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
	// turn_id is non-empty for a supervised run so the client can open its live conversation; "" otherwise.
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "turn_id": s.mgr.TurnFor(id)})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": s.mgr.Cancel(id)})
}

// restartJob re-runs a finished (failed/cancelled) job as a new job with the same parameters and returns
// the new job id, so the UI can navigate to it. POST /api/jobs/{id}/restart
func (s *Server) restartJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	newID, err := s.mgr.Restart(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": newID})
}

// refineJob re-finishes a completed run under the AI supervisor (no re-stack) as a new job, and returns
// the new job id so the UI can follow its live iteration stream. The optional body tunes the loop.
// POST /api/jobs/{id}/refine  { "max_iters"?: int, "tier"?: "A"|"B"|"C", "allow_restack"?: bool }
func (s *Server) refineJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	var req job.RefineRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			badRequest(w, "invalid body")
			return
		}
	}
	req.RunDir = "" // never trust a client path; Refine resolves it from the source run
	newID, err := s.mgr.Refine(r.Context(), id, req)
	if err != nil {
		serverError(w, err)
		return
	}
	// A refine is always supervised, so turn_id is set — the UI opens its live conversation immediately.
	writeJSON(w, http.StatusAccepted, map[string]any{"id": newID, "turn_id": s.mgr.TurnFor(newID)})
}

// listJobs returns a page of jobs newest-first (id desc = date desc) with the total, so the Tasks page
// paginates ("load more") instead of loading the entire history. GET /api/jobs?offset=&limit=
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	offset := clampAtoi(r.URL.Query().Get("offset"), 0, 0, 1<<30)
	limit := clampAtoi(r.URL.Query().Get("limit"), 20, 1, 100)
	jobs, err := s.store.ListJobs(r.Context(), limit, offset)
	if err != nil {
		serverError(w, err)
		return
	}
	total, _ := s.store.CountJobs(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": jobs, "total": total, "offset": offset, "limit": limit,
	})
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

// jobIterations returns a run's AI-supervised finish iterations (tier, scores, defects, chosen), so the
// agent (and UI) can read a refine's history. GET /api/jobs/{id}/iterations
func (s *Server) jobIterations(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	iters, err := s.store.ListFinishIterations(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"iterations": iters})
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
	// Local-first; if the local copy was freed to S3, pull it from the output mirror on demand.
	served, ok := s.ensureServable(r.Context(), r, abs, s.cfg.OutputDir, "output")
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Result images live at STABLE paths (e.g. output/<obj>_stack.png), so a re-run overwrites the same
	// URL. Force revalidation (ServeFile still answers 304 via Last-Modified when unchanged) so a fresh
	// stack/render is never masked by a cached copy from the previous run.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, served)
}

// previewFile decodes a capture file (FITS/TIFF/raw/PNG/JPEG) under the data dir into a downsampled,
// linearly-normalized 16-bit buffer the viewer stretches client-side. Streams a compact binary body
// (header + uint16 samples; see preview.Preview.Encode), not JSON.
func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.withinData(r.URL.Query().Get("path"))
	if !ok {
		badRequest(w, "path must be inside the data directory")
		return
	}
	if !preview.SupportedExt(abs) {
		badRequest(w, "unsupported file type for preview")
		return
	}
	// Local-first; if the capture was freed to S3 (or lives only on S3), pull it from the data mirror.
	served, ok := s.ensureServable(r.Context(), r, abs, s.cfg.DataDir, "data")
	if !ok {
		http.NotFound(w, r)
		return
	}
	maxEdge := s.cfg.PreviewMaxEdge
	if q := r.URL.Query().Get("max"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			maxEdge = n
		}
	}
	pv, err := preview.Load(r.Context(), served, maxEdge)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	_ = pv.Encode(w)
}

// serveThumb returns a small JPEG thumbnail of a run output image. The gallery uses this instead of the
// full-resolution PNG — the page's main slowdown. Confined to the output dir and disk-cached.
func (s *Server) serveThumb(w http.ResponseWriter, r *http.Request) {
	abs, ok := s.within(r.URL.Query().Get("path"), s.cfg.OutputDir)
	if !ok {
		badRequest(w, "path must be inside the output directory")
		return
	}
	// Local-first; if the result PNG was freed to S3, pull it from the output mirror before thumbnailing.
	served, ok := s.ensureServable(r.Context(), r, abs, s.cfg.OutputDir, "output")
	if !ok {
		http.NotFound(w, r)
		return
	}
	dim := clampAtoi(r.URL.Query().Get("w"), 480, 32, 1024)
	data, err := thumb.Cached(filepath.Join(s.cfg.WorkDir, "cache", "thumbs"), served, dim, 80)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// clampAtoi parses s as an int (falling back to def) and clamps the result into [lo, hi].
func clampAtoi(s string, def, lo, hi int) int {
	n := def
	if v, err := strconv.Atoi(s); err == nil {
		n = v
	}
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
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

// listRuns scans the output directory for run.json records so any past run can be reopened from disk,
// independent of the database (e.g. CLI runs). Results are paginated (newest first) so a large gallery
// stays fast: every run is cheaply stat-ed for ordering, but only the requested page is read+summarized.
// runFileRef points at one run's run.json for the gallery: either on local disk (s3Key == "") or, when the
// local output tree was freed, only on the S3 output mirror (s3Key set — read on demand for the page).
type runFileRef struct {
	path  string // absolute local run.json path (the path it occupies or, for S3-only runs, would occupy)
	mtime int64
	s3Key string
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	outAbs, err := filepath.Abs(s.cfg.OutputDir)
	if err != nil {
		serverError(w, err)
		return
	}
	matches, _ := filepath.Glob(filepath.Join(outAbs, "*", "*", "run.json"))

	// Order by mtime (newest first) with a cheap Stat per file — no run.json is read here.
	files := make([]runFileRef, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, p := range matches {
		var mtime int64
		if info, err := os.Stat(p); err == nil {
			mtime = info.ModTime().UnixMilli()
		}
		files = append(files, runFileRef{path: p, mtime: mtime})
		seen[p] = true
	}

	// S3 fallback: fold in runs whose local output tree was freed (present only on the S3 mirror). Their
	// run.json is read from S3 on demand for the requested page only. Requires a chosen bucket; soft-fails.
	// The config honors ?conn= so the mirror is read from the connection the bucket was chosen under.
	q := r.URL.Query()
	var s3cfg s3store.Config
	if bucket := q.Get("bucket"); bucket != "" {
		if cfg, err := s.s3ConfigForRequest(r); err == nil && cfg.Configured() {
			s3cfg = cfg
			for _, ref := range s.s3OutputRuns(r.Context(), cfg, bucket, q.Get("prefix"), outAbs) {
				if seen[ref.path] {
					continue
				}
				seen[ref.path] = true
				files = append(files, ref)
			}
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })

	total := len(files)
	offset := clampAtoi(q.Get("offset"), 0, 0, total)
	limit := clampAtoi(q.Get("limit"), 12, 1, 100)
	end := offset + limit
	if end > total {
		end = total
	}

	runs := make([]runSummary, 0, end-offset)
	for _, f := range files[offset:end] {
		if f.s3Key != "" {
			runs = append(runs, s.summarizeS3Run(r.Context(), s3cfg, q.Get("bucket"), f.s3Key, f.path, f.mtime))
		} else {
			runs = append(runs, summarizeRun(f.path)) // ReadFile only for the page
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs": runs, "total": total, "offset": offset, "limit": limit,
	})
}

func summarizeRun(runJSONPath string) runSummary {
	var mtime int64
	if info, err := os.Stat(runJSONPath); err == nil {
		mtime = info.ModTime().UnixMilli()
	}
	data, _ := os.ReadFile(runJSONPath)
	return summarizeRunBytes(data, runJSONPath, mtime)
}

// summarizeRunBytes derives a run summary from run.json bytes (empty when unreadable) and the local path the
// run occupies — shared by the on-disk gallery and the S3-mirror fallback (summarizeS3Run). Output paths in
// run.json are absolute under OutputDir, so its previews resolve through local-first/S3-fallback serving.
func summarizeRunBytes(data []byte, runJSONPath string, mtimeMs int64) runSummary {
	sum := runSummary{
		Dir:         filepath.Dir(runJSONPath),
		RunJSON:     runJSONPath,
		RunID:       filepath.Base(filepath.Dir(runJSONPath)),
		Object:      filepath.Base(filepath.Dir(filepath.Dir(runJSONPath))),
		CreatedAtMs: mtimeMs,
	}
	if len(data) > 0 {
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
