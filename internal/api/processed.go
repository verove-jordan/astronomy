package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// processedPath is one capture folder of a past processing, with whether it still exists on disk — so
// the UI can cross out (and exclude from re-running) folders that have since been deleted.
type processedPath struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

// processedGroup is one past processing (a job) and the capture folders that fed it. The browser uses
// these to mark already-processed folders, to colour-group folders that shared a processing, and to
// offer the set for re-running (the Import "Processing history"). Mode/Format let a re-run pre-fill them.
type processedGroup struct {
	JobID       int64           `json:"job_id"`
	Kind        string          `json:"kind"`
	Object      string          `json:"object,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Format      string          `json:"format,omitempty"`
	Status      string          `json:"status"`
	CreatedAtMs int64           `json:"created_at_ms"`
	Paths       []processedPath `json:"paths"`
}

// processed reports, per past job, the capture folders it processed (with on-disk existence). GET /api/processed
func (s *Server) processed(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), 500)
	if err != nil {
		serverError(w, err)
		return
	}
	exists := dirExistsCache() // a folder is shared by many jobs — stat it only once per request
	groups := make([]processedGroup, 0, len(jobs))
	for _, j := range jobs {
		var p struct {
			Path   string   `json:"path"`
			Paths  []string `json:"paths"`
			Mode   string   `json:"mode"`
			Format string   `json:"format"`
		}
		_ = json.Unmarshal(j.Params, &p)
		raw := p.Paths
		if len(raw) == 0 && p.Path != "" {
			raw = []string{p.Path}
		}
		// Keep only real filesystem folders (skip the synthetic "s3://…" live-stacking key).
		dirs := make([]processedPath, 0, len(raw))
		for _, pp := range raw {
			if pp != "" && !strings.HasPrefix(pp, "s3://") {
				dirs = append(dirs, processedPath{Path: pp, Exists: exists(pp)})
			}
		}
		if len(dirs) == 0 {
			continue
		}
		var res struct {
			Object string `json:"object"`
		}
		_ = json.Unmarshal(j.Result, &res)
		groups = append(groups, processedGroup{
			JobID:       j.ID,
			Kind:        j.Kind,
			Object:      res.Object,
			Mode:        p.Mode,
			Format:      p.Format,
			Status:      j.Status,
			CreatedAtMs: j.CreatedAt,
			Paths:       dirs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

// dirExistsCache returns a memoized "does this path exist on disk?" check, so a folder shared across
// many past jobs is stat-ed only once per request.
func dirExistsCache() func(string) bool {
	seen := make(map[string]bool)
	return func(path string) bool {
		if v, ok := seen[path]; ok {
			return v
		}
		_, err := os.Stat(path)
		v := err == nil
		seen[path] = v
		return v
	}
}
