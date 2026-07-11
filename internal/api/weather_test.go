package api

import (
	"encoding/json"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/weather"
)

const omTestBody = `"time":["2026-06-30T20:00","2026-06-30T21:00","2026-06-30T22:00"],` +
	`"cloud_cover":[10,80,5],"temperature_2m":[15,14,13],"dew_point_2m":[8,12,7]`

// weatherTestServer builds an API Server whose weather provider talks to a fake Open-Meteo (the other
// feeds are left unconfigured, so they soft-fail and only the backbone contributes).
func weatherTestServer(t *testing.T, withProvider bool) *Server {
	t.Helper()
	cfg := &config.Config{
		WorkDir: t.TempDir(), LatDeg: 48.86, LonDeg: 2.35, Timezone: "UTC",
		WeatherGridSize: 4, WeatherGridRadiusDeg: 2, WeatherCacheTTLMin: 30,
	}
	if withProvider {
		om := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			lat := r.URL.Query().Get("latitude")
			if strings.Contains(lat, ",") {
				n := strings.Count(lat, ",") + 1
				objs := make([]string, n)
				for i := range objs {
					objs[i] = `{"hourly":{` + omTestBody + `}}`
				}
				_, _ = io.WriteString(w, "["+strings.Join(objs, ",")+"]")
				return
			}
			_, _ = io.WriteString(w, `{"hourly":{`+omTestBody+`}}`)
		}))
		t.Cleanup(om.Close)
		cfg.WeatherOpenMeteoURL = om.URL
	}
	s := &Server{cfg: cfg}
	if withProvider {
		s.weather = weather.New(cfg)
	}
	return s
}

func TestSkyWeather_ReturnsForecast(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather?at=2026-06-30T21:00:00Z&lat=48.86&lon=2.35", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp weatherResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Forecast.Hours, 3)
	assert.InDelta(t, 10, resp.Forecast.Hours[0].CloudPct, 0.01)
	assert.Equal(t, "low", resp.Forecast.Hours[0].DewRisk)
	assert.Contains(t, resp.Forecast.Sources, "Open-Meteo")
	assert.Equal(t, "query", resp.Query.Location.Source)
}

func TestSkyWeather_BadLatLon(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather?lat=200", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSkyWeather_NilProviderIsSafe(t *testing.T) {
	h := weatherTestServer(t, false).Handler() // partial server, no provider
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather?lat=48&lon=2", nil))
	require.Equal(t, http.StatusOK, rec.Code) // weatherAt nil-guard → empty forecast, never panics
	var resp weatherResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Empty(t, resp.Forecast.Hours)
}

func TestSkyWeatherGrid_ReturnsCube(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather/grid?lat=48.86&lon=2.35&layers=clouds", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp weatherGridResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Greater(t, resp.Grid.Nx, 0)
	require.Contains(t, resp.Grid.Layers, "clouds")
	require.Len(t, resp.Grid.Layers["clouds"], 3, "one frame per timestep")
	assert.Len(t, resp.Grid.Layers["clouds"][0], resp.Grid.Nx*resp.Grid.Ny, "nx*ny cells")
}

// TestSkyWeatherGrid_StableUnderPan is the end-to-end guard for the reported bug: panning the map must not
// make the overlay drift. A sub-cell pan of the request centre must return a grid on the SAME global
// lattice — same cell size, and edges shifted by a whole number of cells (never a fraction) — so
// overlapping cells keep identical coordinates and a location's value stays put instead of chasing focus.
func TestSkyWeatherGrid_StableUnderPan(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	get := func(lat, lon string) weather.Grid {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/sky/weather/grid?lat="+lat+"&lon="+lon+"&layers=clouds", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var resp weatherGridResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp.Grid
	}
	a := get("48.86", "2.35")
	b := get("48.86", "2.65") // a 0.3° pan — smaller than the ~1° cell at this zoom

	stepA := (a.BBox[2] - a.BBox[0]) / float64(a.Nx-1)
	stepB := (b.BBox[2] - b.BBox[0]) / float64(b.Ny-1)
	assert.InDelta(t, stepA, stepB, 1e-9, "a sub-cell pan keeps the cell size")
	// Co-aligned: each edge of b differs from a's by an exact multiple of the step (same grid lines).
	for i, edge := range []string{"west", "south", "east", "north"} {
		assert.InDelta(t, 0, math.Remainder(b.BBox[i]-a.BBox[i], stepA), 1e-6, "co-aligned %s", edge)
	}
}

func TestSkyWeatherGridFrames_AxisAndETag(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/sky/weather/grid/frames?lat=48.86&lon=2.35&layers=clouds", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	etag := rec.Header().Get("ETag")
	assert.NotEmpty(t, etag, "frames carry a weak ETag for conditional refetch")
	assert.Contains(t, rec.Header().Get("Cache-Control"), "max-age")

	var resp weatherFramesResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Len(t, resp.Timesteps, 3, "the time axis has one entry per timestep")
	assert.NotZero(t, resp.IssuedMs)
	// The frames payload carries only the axis + coverage — the heavy floats live in the tiles.
	assert.NotEqual(t, [4]float64{}, resp.BBox)

	// A conditional refetch of the unchanged forecast is a 304.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sky/weather/grid/frames?lat=48.86&lon=2.35&layers=clouds", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusNotModified, rec2.Code)
}

func TestSkyWeatherTile_RendersPNG(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	// time=0 → FrameIndex picks the nearest (first) frame; (z6,x32,y21) is near the fake cube's centre.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather/tiles/clouds/0/6/32/21", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Cache-Control"), "max-age", "rendered tiles are browser-cacheable")

	img, err := png.Decode(rec.Body)
	require.NoError(t, err)
	assert.Equal(t, 256, img.Bounds().Dx())
	assert.Equal(t, 256, img.Bounds().Dy())
}

func TestSkyWeatherTile_Validation(t *testing.T) {
	h := weatherTestServer(t, true).Handler()
	for _, path := range []string{
		"/api/sky/weather/tiles/bogus/0/6/32/21",  // unknown metric
		"/api/sky/weather/tiles/clouds/x/6/32/21", // non-numeric time
		"/api/sky/weather/tiles/clouds/0/z/32/21", // non-numeric z
		"/api/sky/weather/tiles/clouds/0/6/x/21",  // non-numeric x
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
}

func TestSkyWeatherTile_DegradedServesTransparent(t *testing.T) {
	// No provider → an empty cube (no timesteps). The tile degrades to a transparent, briefly-cached PNG
	// (a 200) rather than a 502: an error status would trip the client's cache-busting tile-retry storm,
	// which only burns more of the upstream's daily quota and keeps the outage alive. The short max-age lets
	// the overlay self-heal within a minute on recovery, without caching a long-lived hole.
	h := weatherTestServer(t, false).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/weather/tiles/clouds/0/6/32/21", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	cc := rec.Header().Get("Cache-Control")
	assert.Contains(t, cc, "max-age", "brief cache, not a permanent hole")
	assert.NotContains(t, cc, "no-store", "degraded tile is briefly cacheable, so the client doesn't hammer")

	img, err := png.Decode(rec.Body)
	require.NoError(t, err)
	_, _, _, a := img.At(img.Bounds().Min.X, img.Bounds().Min.Y).RGBA()
	assert.Zero(t, a, "degraded tile is fully transparent (no overlay drawn)")
}
