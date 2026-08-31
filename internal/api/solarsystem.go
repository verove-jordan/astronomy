// The solar-system endpoints.
//
// Like the galaxy cloud and unlike everything else under /api, none of this belongs to a run: it is
// the solar system, which is the same for every user of every engine. So the model is served once,
// cached hard in the browser, and animated there — the engine is asked for a position only when a
// number has to be printed for someone to trust.
package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/solarsystem"
)

// solarSystemCacheControl asks the browser to revalidate every time, and the ETag makes that a
// 40-byte 304 rather than a re-download.
//
// A long max-age would be wrong here even though the model itself never changes: the manifest also
// carries which surface maps this engine has on disk, and the whole point of the download recipe is
// that running it makes the page photographic. Cached for a week, it would not — for a week, with
// nothing to say why.
const solarSystemCacheControl = "public, max-age=0, must-revalidate"

// solarTextureCacheControl is longer still — a surface map never changes once downloaded, and these
// are megabytes each.
const solarTextureCacheControl = "public, max-age=2592000"

var (
	solarTexturesOnce sync.Once
	solarTextures     []string
)

// availableTextures scans the texture directory once per process. Re-running the download recipe
// while the engine is up therefore needs a restart to be noticed, which is the right trade: the
// alternative is a directory scan on every manifest request forever.
func (s *Server) availableTextures() []string {
	solarTexturesOnce.Do(func() {
		solarTextures = solarsystem.Textures(solarsystem.TextureDir(s.cfg.WorkDir))
	})
	return solarTextures
}

// solarSystemBodies serves the model the browser animates from.
// GET /api/solarsystem/bodies
func (s *Server) solarSystemBodies(w http.ResponseWriter, r *http.Request) {
	if v := r.URL.Query().Get("v"); v != "" {
		want, err := strconv.Atoi(v)
		if err != nil {
			badRequest(w, "v must be an integer")
			return
		}
		// Answering a mismatch with 409 rather than with the bytes is deliberate: the viewer refuses a
		// version it does not know, so serving it anyway would turn an engine/frontend skew into a
		// silently empty solar system instead of a message saying which side is out of date.
		if want != solarsystem.ManifestVersion {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "the engine's solar-system model does not match this page — reload it",
				"code":    "solarsystem_version_mismatch",
				"engine":  solarsystem.ManifestVersion,
				"browser": want,
			})
			return
		}
	}

	data, etag, err := solarsystem.Encoded(s.availableTextures())
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", solarSystemCacheControl)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if _, err := w.Write(data); err != nil {
		// The client hung up mid-transfer. Nothing to recover and nothing to report.
		return
	}
}

// solarSystemState answers where everything is at one instant, for one observing site — the
// authoritative readout behind the info panel.
// GET /api/solarsystem/state?t=<unix ms>&lat=&lon=&elevation=
func (s *Server) solarSystemState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	when := time.Now().UTC()
	if raw := q.Get("t"); raw != "" {
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			badRequest(w, "t must be a Unix timestamp in milliseconds")
			return
		}
		when = time.UnixMilli(ms).UTC()
	}
	if y := when.Year(); y < astro.ElementsFrom || y > astro.ElementsTo {
		badRequest(w, "the orbital model is only defended for "+
			strconv.Itoa(astro.ElementsFrom)+"–"+strconv.Itoa(astro.ElementsTo)+
			"; refusing to answer for "+strconv.Itoa(y))
		return
	}

	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "invalid location: lat must be -90..90 and lon -180..180")
		return
	}

	writeJSON(w, http.StatusOK, solarsystem.StateAt(when, solarsystem.Site{
		LatDeg:     lat,
		LonDeg:     lon,
		ElevationM: floatParam(q, "elevation", s.cfg.ElevationM),
	}))
}

// solarSystemTexture streams one downloaded surface map. A 404 here is ordinary, not a failure: the
// page falls back to procedural shading for any body whose texture was never downloaded.
// GET /api/solarsystem/texture?key=mars
func (s *Server) solarSystemTexture(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	path, ok := solarsystem.TexturePath(solarsystem.TextureDir(s.cfg.WorkDir), key)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "no texture for " + key,
			"code":  "texture_not_downloaded",
		})
		return
	}
	w.Header().Set("Cache-Control", solarTextureCacheControl)
	http.ServeFile(w, r, path)
}
