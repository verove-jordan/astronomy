package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/align"
	"github.com/verove-jordan/astronomy/internal/config"
)

func alignTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{cfg: &config.Config{LatDeg: 48.857, LonDeg: 2.352, Timezone: "Europe/Paris"}}
}

func TestSkyAlign_ReturnsOrderedPlan(t *testing.T) {
	h := alignTestServer(t).Handler()
	resp := getAlign(t, h, "/api/sky/align?at=2026-01-15T21:00:00Z&count=3&profile=eq-generic")

	assert.Equal(t, "eq-generic", resp.Query.Profile)
	assert.Equal(t, 3, resp.Query.Count)
	require.Len(t, resp.Result.Stars, 3)
	assert.Equal(t, "recommended", resp.Result.Stars[0].Status)
	for i, s := range resp.Result.Stars {
		assert.Equal(t, i+1, s.Order)
		assert.NotEmpty(t, s.Name)
		assert.NotEmpty(t, s.Compass)
	}
}

func TestSkyAlign_RejectedStarExcluded(t *testing.T) {
	h := alignTestServer(t).Handler()
	base := getAlign(t, h, "/api/sky/align?at=2026-01-15T21:00:00Z&count=3")
	require.NotEmpty(t, base.Result.Stars)
	skip := base.Result.Stars[0].Name

	after := getAlign(t, h, "/api/sky/align?at=2026-01-15T21:00:00Z&count=3&rejected="+url.QueryEscape(skip))
	require.Len(t, after.Result.Stars, 3)
	for _, s := range after.Result.Stars {
		assert.NotEqual(t, skip, s.Name)
	}
}

func TestSkyAlign_UnknownProfileDefaults(t *testing.T) {
	h := alignTestServer(t).Handler()
	resp := getAlign(t, h, "/api/sky/align?profile=bogus")
	assert.Equal(t, align.Default().Key, resp.Query.Profile)
}

func TestSkyAlign_BadLatLon(t *testing.T) {
	h := alignTestServer(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/align?lat=200", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func getAlign(t *testing.T, h http.Handler, path string) alignResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp alignResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	return resp
}
