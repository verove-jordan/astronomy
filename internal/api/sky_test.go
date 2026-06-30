package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

func skyTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "messier.csv"),
		[]byte("name,ra,dec,diameter,mag,alias\nM81,148.888,69.065,26.9,6.9,Bode's Galaxy/NGC3031\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ngc.csv"),
		[]byte("name,ra,dec,diameter,mag,alias\nNGC104,6.0238,-72.081,50,4.0,47 Tucanae\n"), 0o644))
	cfg := &config.Config{
		SirilCatalogDir: dir,
		LatDeg:          48.8566,
		LonDeg:          2.3522,
		Timezone:        "UTC",
		FocalLenMM:      740,
		ApertureMM:      100,
		PixelSizeUm:     3.8,
		SensorWpx:       4656,
		SensorHpx:       3520,
	}
	return &Server{cfg: cfg, planner: skyplan.New(dir)}
}

func TestSkyTargets(t *testing.T) {
	h := skyTestServer(t).Handler()

	t.Run("ranks targets and echoes the setup", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?at=2026-03-15T23:00:00Z", nil))
		require.Equal(t, http.StatusOK, rec.Code)

		var resp skyResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, 2, resp.Count)
		assert.Equal(t, "config", resp.Query.Location.Source)
		assert.InDelta(t, 7.4, resp.Query.Equipment.FRatio, 0.01)
		assert.Greater(t, resp.Darkness.DawnUTCMs, resp.Darkness.DuskUTCMs)

		// Sorted by score descending.
		for i := 1; i < len(resp.Targets); i++ {
			assert.GreaterOrEqual(t, resp.Targets[i-1].Score, resp.Targets[i].Score)
		}
		assert.Equal(t, "M81", resp.Targets[0].Name)
	})

	t.Run("query location overrides config", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?lat=40&lon=-74", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var resp skyResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "query", resp.Query.Location.Source)
		assert.InDelta(t, 40.0, resp.Query.Location.Lat, 1e-9)
	})

	t.Run("visual mode recommends an eyepiece per target", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/sky/targets?at=2026-03-15T23:00:00Z&mode=visual&eyepieces=30:68:30mm,10:60:10mm", nil))
		require.Equal(t, http.StatusOK, rec.Code)

		var resp skyResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Equal(t, "visual", resp.Query.Equipment.Mode)
		require.Len(t, resp.Query.Equipment.Eyepieces, 2)

		var m81 *skyplan.Target
		for i := range resp.Targets {
			if resp.Targets[i].Name == "M81" {
				m81 = &resp.Targets[i]
			}
		}
		require.NotNil(t, m81)
		assert.NotEmpty(t, m81.ChosenEyepiece)
		assert.Greater(t, m81.MagX, 0.0)
		assert.Greater(t, m81.TrueFOVDeg, 0.0)
		assert.Greater(t, m81.ExitPupilMM, 0.0)
	})

	t.Run("camera mode JSON omits the visual fields", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?at=2026-03-15T23:00:00Z", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		body := rec.Body.String()
		assert.NotContains(t, body, "chosen_eyepiece")
		assert.NotContains(t, body, "\"mode\"")
		assert.NotContains(t, body, "eyepiece")
	})

	t.Run("out-of-range latitude is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?lat=200", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid 'at' is rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?at=not-a-time", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
