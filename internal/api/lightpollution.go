package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/lightpollution"
)

// lightPollution returns the artificial sky brightness (SQM + Bortle) at a location, for the map's
// site-quality badge. Every parameter is optional and falls back to the configured site.
func (s *Server) lightPollution(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat := floatParam(q, "lat", s.cfg.LatDeg)
	lon := floatParam(q, "lon", s.cfg.LonDeg)
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}
	site, warn := s.siteAt(r.Context(), lat, lon)
	resp := map[string]any{"site": site}
	if warn != "" {
		resp["warning"] = warn
	}
	writeJSON(w, http.StatusOK, resp)
}

// lightPollutionTile proxies one map-overlay tile from the configured upstream (the API key stays
// server-side) and caches it on disk. When no tile source is configured, or the upstream is down, it
// serves a transparent tile so the map simply shows no overlay rather than broken tiles.
func (s *Server) lightPollutionTile(w http.ResponseWriter, r *http.Request) {
	z, err1 := strconv.Atoi(r.PathValue("z"))
	x, err2 := strconv.Atoi(r.PathValue("x"))
	y, err3 := strconv.Atoi(r.PathValue("y"))
	if err1 != nil || err2 != nil || err3 != nil {
		badRequest(w, "invalid tile coordinates")
		return
	}
	if s.lightpollution == nil {
		writeTransparentTile(w, "public, max-age=86400")
		return
	}
	path, err := s.lightpollution.ColoredTile(r.Context(), z, x, y)
	if err != nil {
		if errors.Is(err, lightpollution.ErrNoTileSource) {
			writeTransparentTile(w, "public, max-age=86400") // a stable "no overlay" state, safe to cache
			return
		}
		// A transient upstream/render failure must surface as a real error, never a blank 200: a
		// transparent tile reads as success, so Leaflet caches the hole and never refetches it — leaving
		// a permanent gap in the overlay. A 5xx fires Leaflet's tileerror, which the map retries (see
		// LocationPicker.retryTile). no-store keeps the browser from caching the failure.
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "tile temporarily unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeFile(w, r, path)
}

// siteAt resolves the site's light pollution, tolerating a nil provider (e.g. a partially-built Server
// in tests) by reporting an unknown site — SQM 0, which the scorers read as "no light-pollution penalty".
func (s *Server) siteAt(ctx context.Context, lat, lon float64) (lightpollution.SiteQuality, string) {
	if s.lightpollution == nil {
		return lightpollution.SiteQuality{}, ""
	}
	return s.lightpollution.At(ctx, lat, lon)
}

// writeTransparentTile serves a 1×1 transparent PNG so the map shows no overlay (not broken tiles). The
// caller picks the cache policy: a long max-age for permanent "no source", but no-store for a transient
// upstream failure so the tile is re-fetched (and colored) on the next request instead of sticking blank.
func writeTransparentTile(w http.ResponseWriter, cacheControl string) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", cacheControl)
	_, _ = w.Write(transparentTilePNG)
}

// transparentTilePNG is a 1×1 fully-transparent PNG, encoded once at startup.
var transparentTilePNG = encodeTransparentPNG()

func encodeTransparentPNG() []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 1, 1))) // zero pixels = transparent
	return buf.Bytes()
}
