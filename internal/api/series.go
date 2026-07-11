// Agent improvement series endpoints: durable "keep making this target better" campaigns whose
// attempts are ordinary jobs (linked by series_id). Thin handlers over the job manager.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/store"
)

func (s *Server) createSeries(w http.ResponseWriter, r *http.Request) {
	var body store.AgentSeries
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if body.Object == "" || body.Kind == "" {
		badRequest(w, "object and kind are required")
		return
	}
	id, err := s.mgr.CreateSeries(r.Context(), body)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (s *Server) listSeries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.mgr.ListSeries(r.Context(), limit)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": rows})
}

func (s *Server) getSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	sr, jobs, err := s.mgr.SeriesDetail(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": sr, "jobs": jobs})
}

// setSeriesStatus handles both continue (→ active) and stop (→ stopped).
func (s *Server) setSeriesStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			badRequest(w, "invalid id")
			return
		}
		if err := s.mgr.SetSeriesStatus(r.Context(), id, status); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": status})
	}
}
