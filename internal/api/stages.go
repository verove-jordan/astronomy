package api

// Full-resolution stage downloads: the timeline previews are half-scale PNGs, so these endpoints
// expose the underlying data of each preserved stage at native resolution, as PNG or TIFF. The
// rendered file is returned by path; the client fetches it through GET /api/file like any other
// run artifact.

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// listJobStages returns the exportable full-resolution stages of a finished run, in pipeline order.
// GET /api/jobs/{id}/stages
func (s *Server) listJobStages(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	items, err := s.mgr.StageArtifacts(r.Context(), id)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stages": items})
}

// exportJobStage renders one stage at full resolution and returns the written file's path, which the
// client then downloads via GET /api/file. POST /api/jobs/{id}/stages/export
func (s *Server) exportJobStage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid job id")
		return
	}
	var body struct {
		Key    string `json:"key"`
		Format string `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Format == "" {
		body.Format = "png"
	}
	path, err := s.mgr.ExportStage(r.Context(), id, body.Key, body.Format)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}
