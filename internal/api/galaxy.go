// The Milky Way point cloud endpoint.
//
// Unlike everything else under /api this resource has nothing to do with any particular run: it IS
// the Galaxy, sampled from published structure by internal/scene3d, and only the rotation into a
// given photograph's frame is per-run — which the viewer does on the GPU with a 3×3 uniform. So it is
// generated once per process and cached hard in the browser: one fetch serves every run the user ever
// opens.
package api

import (
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/scene3d"
)

// galaxyCacheControl keeps the cloud in the browser for a week. Not `immutable`: the ETag is what
// makes a re-fetch a 26-byte 304, and leaving revalidation possible means an engine upgrade that
// changes the model reaches a client that has been left open, rather than being pinned out for a year.
const galaxyCacheControl = "public, max-age=604800"

// getGalaxyPoints serves the point cloud as a raw buffer for gl.bufferData.
//
// The optional `v` parameter is the record layout the CALLER can decode. Answering a mismatch with 409
// rather than with the bytes is deliberate: the viewer refuses a version it does not know, so serving
// it anyway would turn an engine/frontend skew into a silently missing Galaxy instead of a message
// saying which side is out of date.
// GET /api/galaxy/points
func (s *Server) getGalaxyPoints(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("v"); v != "" {
		want, err := strconv.Atoi(v)
		if err != nil {
			badRequest(w, "v must be an integer")
			return
		}
		if want != scene3d.GalaxyVersion {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "the engine's galaxy model does not match this page — reload it",
				"code":    "galaxy_version_mismatch",
				"engine":  scene3d.GalaxyVersion,
				"browser": want,
			})
			return
		}
	}

	data, etag := scene3d.GalaxyCloud()
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", galaxyCacheControl)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		// The client hung up mid-transfer. Nothing to recover and nothing to report: the status line is
		// long gone.
		return
	}
}
