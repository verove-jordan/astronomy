package api

import (
	"encoding/json"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/setqa"
)

// setQuality analyzes the selection's light sets for stray-light / gradient artifacts before
// stacking (the Import "check frame sets" button). Synchronous like the align-points estimator:
// the probe budget is capped, so the whole pass stays interactive. POST /api/quality/sets.
func (s *Server) setQuality(w http.ResponseWriter, r *http.Request) {
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
	rep, err := setqa.Analyze(r.Context(), inv, setqa.DefaultOptions())
	if err != nil {
		serverError(w, err)
		return
	}
	if len(rep.Sets) == 0 {
		badRequest(w, "no light frames to analyze")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
