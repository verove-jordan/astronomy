package api

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// skyResponse is the GET /api/sky/targets payload: the effective inputs echoed back, tonight's
// darkness summary, the site's light pollution, and the ranked targets.
type skyResponse struct {
	Query    queryEcho                  `json:"query"`
	Darkness skyplan.DarknessInfo       `json:"darkness"`
	Count    int                        `json:"count"`
	Targets  []skyplan.Target           `json:"targets"`
	Site     lightpollution.SiteQuality `json:"site"`
	Warnings []string                   `json:"warnings,omitempty"`
}

type queryEcho struct {
	AtUTCMs   int64         `json:"at_utc_ms"`
	AtLocal   string        `json:"at_local"`
	Location  locationEcho  `json:"location"`
	Equipment equipmentEcho `json:"equipment"`
	MinAltDeg float64       `json:"min_alt_deg"`
	Twilight  string        `json:"twilight"`
	Limit     int           `json:"limit"`
}

type locationEcho struct {
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	ElevationM float64 `json:"elevation_m"`
	Timezone   string  `json:"timezone"`
	Source     string  `json:"source"` // "config" | "query"
}

type equipmentEcho struct {
	FocalMM            float64        `json:"focal_mm"`
	ApertureMM         float64        `json:"aperture_mm"`
	PixelUm            float64        `json:"pixel_um"`
	SensorWpx          int            `json:"sensor_w_px"`
	SensorHpx          int            `json:"sensor_h_px"`
	ImageScaleArcsecPx float64        `json:"image_scale_arcsec_px"`
	FovWDeg            float64        `json:"fov_w_deg"`
	FovHDeg            float64        `json:"fov_h_deg"`
	FRatio             float64        `json:"f_ratio"`
	BarlowX            float64        `json:"barlow_x"`
	Mode               string         `json:"mode,omitempty"`
	Eyepieces          []eyepieceEcho `json:"eyepieces,omitempty"`
}

// eyepieceEcho echoes one configured eyepiece in the visual-mode equipment block.
type eyepieceEcho struct {
	Label   string  `json:"label"`
	FocalMM float64 `json:"focal_mm"`
	AFOVDeg float64 `json:"afov_deg"`
}

// skyTargets ranks the deep-sky catalog for imaging tonight from the observer's location and rig.
// Every parameter is optional and falls back to the configured defaults.
func (s *Server) skyTargets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	at := time.Now().UTC()
	if v := q.Get("at"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			badRequest(w, "invalid 'at' (want RFC3339)")
			return
		}
		at = parsed.UTC()
	}

	loc := s.cfg.Location()
	prm := skyplan.Params{
		At:            at,
		Lat:           floatParam(q, "lat", s.cfg.LatDeg),
		Lon:           floatParam(q, "lon", s.cfg.LonDeg),
		ElevationM:    floatParam(q, "elevation_m", s.cfg.ElevationM),
		MinAltDeg:     floatParam(q, "min_alt", 30),
		Twilight:      twilightParam(q.Get("twilight")),
		Limit:         intParam(q, "limit", 50),
		TypeFilter:    q.Get("type"),
		CatalogFilter: q.Get("catalog"),
		Location:      loc,
		Mode:          modeParam(q.Get("mode")),
		Eyepieces:     eyepiecesParam(q.Get("eyepieces")),
		Optics: skyplan.Optics{
			FocalMM:    floatParam(q, "focal_mm", s.cfg.FocalLenMM),
			ApertureMM: floatParam(q, "aperture_mm", s.cfg.ApertureMM),
			PixelUm:    floatParam(q, "pixel_um", s.cfg.PixelSizeUm),
			SensorWpx:  intParam(q, "sensor_w", s.cfg.SensorWpx),
			SensorHpx:  intParam(q, "sensor_h", s.cfg.SensorHpx),
			BarlowX:    floatParam(q, "barlow", s.cfg.BarlowX),
		},
	}
	// Weights are left zero so the planner picks DefaultWeights (camera) or VisualWeights by mode.
	if prm.Mode == "visual" && len(prm.Eyepieces) == 0 {
		prm.Eyepieces = eyepiecesParam(s.cfg.EyepieceKit)
	}
	if prm.Lat < -90 || prm.Lat > 90 || prm.Lon < -180 || prm.Lon > 180 {
		badRequest(w, "lat/lon out of range")
		return
	}

	// Resolve the site's light pollution once and fold it into every target's score (soft-failing —
	// At always yields a value plus an optional warning, so scoring never blocks on the network).
	site, siteWarn := s.siteAt(r.Context(), prm.Lat, prm.Lon)
	prm.SiteSQM = site.SQM

	res, err := s.planner.Plan(r.Context(), prm)
	if err != nil {
		serverError(w, err)
		return
	}
	warnings := res.Warnings
	if siteWarn != "" {
		warnings = append(warnings, siteWarn)
	}
	writeJSON(w, http.StatusOK, skyResponse{
		Query:    queryEchoOf(prm, at, loc, s.cfg.Timezone, q),
		Darkness: res.Darkness,
		Count:    res.Count,
		Targets:  res.Targets,
		Site:     site,
		Warnings: warnings,
	})
}

func queryEchoOf(prm skyplan.Params, at time.Time, loc *time.Location, tz string, q url.Values) queryEcho {
	source := "config"
	if q.Get("lat") != "" || q.Get("lon") != "" {
		source = "query"
	}
	fovW, fovH := prm.Optics.FOV()
	eq := equipmentEcho{
		FocalMM:            prm.Optics.FocalMM,
		ApertureMM:         prm.Optics.ApertureMM,
		PixelUm:            prm.Optics.PixelUm,
		SensorWpx:          prm.Optics.SensorWpx,
		SensorHpx:          prm.Optics.SensorHpx,
		ImageScaleArcsecPx: round(prm.Optics.ImageScale(), 2),
		FovWDeg:            round(fovW, 3),
		FovHDeg:            round(fovH, 3),
		FRatio:             round(prm.Optics.FRatio(), 2),
		BarlowX:            prm.Optics.BarlowX,
	}
	if prm.Mode == "visual" {
		eq.Mode = "visual"
		for _, e := range prm.Eyepieces {
			eq.Eyepieces = append(eq.Eyepieces, eyepieceEcho{Label: e.Label, FocalMM: e.FocalMM, AFOVDeg: e.AFOVDeg})
		}
	}
	return queryEcho{
		AtUTCMs: at.UnixMilli(),
		AtLocal: at.In(loc).Format("2006-01-02 15:04"),
		Location: locationEcho{
			Lat:        prm.Lat,
			Lon:        prm.Lon,
			ElevationM: prm.ElevationM,
			Timezone:   tz,
			Source:     source,
		},
		Equipment: eq,
		MinAltDeg: prm.MinAltDeg,
		Twilight:  prm.Twilight,
		Limit:     prm.Limit,
	}
}

// geoResult is one address-search hit.
type geoResult struct {
	Label string  `json:"label"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
}

var geocodeClient = &http.Client{Timeout: 8 * time.Second}

// geocode proxies an address lookup to OpenStreetMap's Nominatim (server-side, so the browser avoids
// CORS and a proper User-Agent is sent per Nominatim's usage policy). The UI degrades to manual
// lat/lon entry when this is unavailable.
func (s *Server) geocode(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		badRequest(w, "missing 'q'")
		return
	}
	endpoint := "https://nominatim.openstreetmap.org/search?format=jsonv2&limit=5&q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		serverError(w, err)
		return
	}
	req.Header.Set("User-Agent", "AstroStack/1.0 (deep-sky visibility planner)")
	resp, err := geocodeClient.Do(req)
	if err != nil {
		serverError(w, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "geocoding service error"})
		return
	}
	var raw []struct {
		DisplayName string `json:"display_name"`
		Lat         string `json:"lat"`
		Lon         string `json:"lon"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		serverError(w, err)
		return
	}
	out := make([]geoResult, 0, len(raw))
	for _, g := range raw {
		lat, errLat := strconv.ParseFloat(g.Lat, 64)
		lon, errLon := strconv.ParseFloat(g.Lon, 64)
		if errLat != nil || errLon != nil {
			continue
		}
		out = append(out, geoResult{Label: g.DisplayName, Lat: lat, Lon: lon})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func floatParam(q url.Values, key string, def float64) float64 {
	if v := q.Get(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func intParam(q url.Values, key string, def int) int {
	if v := q.Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func twilightParam(v string) string {
	if v == "nautical" {
		return "nautical"
	}
	return "astro"
}

// modeParam normalizes the observing-mode query value to "visual" or "camera" (the default).
func modeParam(v string) string {
	if v == "visual" {
		return "visual"
	}
	return "camera"
}

// eyepiecesParam parses a comma-separated visual kit of "focalMM:afovDeg[:label]" items
// (e.g. "30:68:30mm,10:60"). Malformed or non-positive entries are skipped; a missing label defaults
// to "{focal}mm".
func eyepiecesParam(raw string) []skyplan.Eyepiece {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var kit []skyplan.Eyepiece
	for _, item := range strings.Split(raw, ",") {
		fields := strings.Split(item, ":")
		if len(fields) < 2 {
			continue
		}
		focal, errFocal := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
		afov, errAFOV := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		if errFocal != nil || errAFOV != nil || focal <= 0 || afov <= 0 {
			continue
		}
		label := ""
		if len(fields) >= 3 {
			label = strings.TrimSpace(fields[2])
		}
		if label == "" {
			label = strconv.FormatFloat(focal, 'f', -1, 64) + "mm"
		}
		kit = append(kit, skyplan.Eyepiece{FocalMM: focal, AFOVDeg: afov, Label: label})
	}
	return kit
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
