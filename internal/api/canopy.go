package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/canopy"
)

// canopyAtlasStatus reports the installed canopy-atlas coverage + any in-progress download, for the dark-sky
// finder's "download canopy for this area" panel to render and poll. GET /api/sky/canopy/atlas
func (s *Server) canopyAtlasStatus(w http.ResponseWriter, _ *http.Request) {
	if s.canopy == nil {
		writeJSON(w, http.StatusOK, canopy.BuildState{Status: "idle"})
		return
	}
	writeJSON(w, http.StatusOK, s.canopy.BuildStateNow())
}

// canopyBuildAtlas starts a background download + build of the canopy-height atlas for a region preset or an
// explicit bbox (the drawn area), then hot-reloads it so the finder immediately folds trees into the
// horizon. POST /api/sky/canopy/atlas
func (s *Server) canopyBuildAtlas(w http.ResponseWriter, r *http.Request) {
	if s.canopy == nil {
		serverError(w, fmt.Errorf("canopy provider unavailable"))
		return
	}
	var body struct {
		Region string  `json:"region"`
		MinLat float64 `json:"min_lat"`
		MinLon float64 `json:"min_lon"`
		MaxLat float64 `json:"max_lat"`
		MaxLon float64 `json:"max_lon"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}

	var b canopy.Bounds
	if body.Region != "" {
		bb, err := canopy.ResolveBounds(body.Region, "")
		if err != nil {
			badRequest(w, err.Error())
			return
		}
		b = bb
	} else {
		b = canopy.Bounds{MinLat: body.MinLat, MinLon: body.MinLon, MaxLat: body.MaxLat, MaxLon: body.MaxLon}
		if b.MaxLat <= b.MinLat || b.MaxLon <= b.MinLon {
			badRequest(w, "bbox max must exceed min (or pass a region)")
			return
		}
	}

	if err := s.canopy.StartBuild(b); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, s.canopy.BuildStateNow())
}
