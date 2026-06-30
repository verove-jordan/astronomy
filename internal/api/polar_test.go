package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkyPolar(t *testing.T) {
	h := skyTestServer(t).Handler()

	t.Run("computes the reticle position", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/sky/polar?at=2026-06-29T22:00:00Z&lat=48.28&lon=2.78", nil))
		require.Equal(t, http.StatusOK, rec.Code)

		var resp polarResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "north", resp.Result.Hemisphere)
		assert.Equal(t, "Polaris", resp.Result.PoleStarName)
		assert.InDelta(t, 48.28, resp.Result.AltDeg, 1.5) // pole-star altitude ≈ latitude
		assert.InDelta(t, 0.65, resp.Result.SeparationDeg, 0.15)
		assert.GreaterOrEqual(t, resp.Result.ClockHour, 0.0)
		assert.Less(t, resp.Result.ClockHour, 12.0)
		assert.Equal(t, "query", resp.Query.Location.Source)
	})

	t.Run("southern site uses σ Octantis", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/polar?lat=-33.9&lon=151.2", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var resp polarResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "south", resp.Result.Hemisphere)
		assert.Equal(t, "σ Octantis", resp.Result.PoleStarName)
	})

	t.Run("out-of-range latitude is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/polar?lat=120", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid 'at' is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/polar?at=not-a-time", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
