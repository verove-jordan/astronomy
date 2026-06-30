package api

import (
	"encoding/json"
	"io"
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
	assert.Equal(t, 4, resp.Grid.Nx)
	require.Contains(t, resp.Grid.Layers, "clouds")
	require.Len(t, resp.Grid.Layers["clouds"], 3, "one frame per timestep")
	assert.Len(t, resp.Grid.Layers["clouds"][0], 16, "nx*ny cells")
}
