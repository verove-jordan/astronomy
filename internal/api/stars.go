// Star annotation endpoints: count the stars on a completed run's linear master and label the
// named stars/DSOs in final-image pixels (internal/annotate). Compute is SYNCHRONOUS — the common
// path is 1–3 s of pure Go, the worst path adds one bounded Siril plate-solve — and the result is
// cached as <runDir>/stars.json, so it is deliberately NOT a job (no Tasks row, no SSE dance).
// stars.json stays local-only by design: the S3 results mirror is job-driven, and this is a cheap
// derived cache — if the run dir was freed, GET 404s and a re-POST recomputes (the masters come
// back transparently through ensureServable).
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/store"
)

// starsComputeTimeout bounds one annotation pass (detection + at most one 60 s Siril solve).
const starsComputeTimeout = 90 * time.Second

// starsRun looks up the job and resolves the run directory + mode, mapping every failure to the
// HTTP status the handlers share.
func (s *Server) starsRun(ctx context.Context, w http.ResponseWriter, r *http.Request) (runDir, mode string, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id", "code": "invalid_job_id"})
		return "", "", false
	}
	j, err := s.store.GetJob(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found", "code": "job_not_found"})
		return "", "", false
	}
	if j.Status != store.JobSucceeded {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run is not finished", "code": "run_not_ready"})
		return "", "", false
	}
	var params struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal(j.Params, &params)
	if params.Mode == "planetary" {
		writeJSON(w, http.StatusUnprocessableEntity,
			map[string]string{"error": "planetary runs have no star field", "code": "stars_unsupported_mode"})
		return "", "", false
	}
	runDir = jobRunDir(j.Result)
	if runDir == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "run has no output directory", "code": "run_not_ready"})
		return "", "", false
	}
	return runDir, params.Mode, true
}

// jobRunDir extracts the run directory from a job's result JSON (mirrors the job package's
// unexported helper — output_dir for pipeline runs, dir(out_base) for planetary/video ones).
func jobRunDir(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var r struct {
		OutputDir string `json:"output_dir"`
		OutBase   string `json:"out_base"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if r.OutputDir != "" {
		return r.OutputDir
	}
	if r.OutBase != "" {
		return filepath.Dir(r.OutBase)
	}
	return ""
}

// computeStars runs the annotation for a completed run and returns stars.json. Unsolvable fields
// are 200 with solved:false (the count is still valid); only structural problems are errors.
// POST /api/jobs/{id}/stars
func (s *Server) computeStars(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), starsComputeTimeout)
	defer cancel()
	runDir, mode, ok := s.starsRun(ctx, w, r)
	if !ok {
		return
	}

	solve, _ := postprocess.SolveSpccFromConfig(s.cfg)
	opts := annotate.Options{
		RunDir: runDir,
		Mode:   mode,
		Locate: func(rel string) (string, bool) {
			return s.ensureServable(ctx, r, filepath.Join(runDir, rel), s.cfg.OutputDir, "output")
		},
		Runner:     s.sirilRunner,
		Solve:      solve,
		CatalogDir: s.cfg.SirilCatalogDir,
	}
	// Concurrent POSTs on one run (double-click, two tabs) share a single computation.
	v, err, _ := s.starsFlight.Do(runDir, func() (any, error) {
		return annotate.Run(ctx, opts)
	})
	if err != nil {
		switch {
		case errors.Is(err, annotate.ErrUnsupportedMode):
			writeJSON(w, http.StatusUnprocessableEntity,
				map[string]string{"error": err.Error(), "code": "stars_unsupported_mode"})
		case errors.Is(err, annotate.ErrNoMaster):
			writeJSON(w, http.StatusUnprocessableEntity,
				map[string]string{"error": "no persisted linear master for this run", "code": "stars_no_linear_master"})
		case errors.Is(err, annotate.ErrNoFinal):
			writeJSON(w, http.StatusUnprocessableEntity,
				map[string]string{"error": "no final image for this run", "code": "stars_no_final_image"})
		default:
			serverError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// getStars returns the cached stars.json for a run, or 404 when never computed.
// GET /api/jobs/{id}/stars
func (s *Server) getStars(w http.ResponseWriter, r *http.Request) {
	runDir, _, ok := s.starsRun(r.Context(), w, r)
	if !ok {
		return
	}
	res, found := annotate.Load(runDir)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "stars not computed yet", "code": "stars_not_computed"})
		return
	}
	writeJSON(w, http.StatusOK, res)
}
