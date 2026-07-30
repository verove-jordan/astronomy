package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/mosaicplan"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// mosaicTestServer is skyTestServer with M31 in the catalogue — the planner golden target (the
// embedded OpenNGC overlay supplies its minor axis + position angle).
func mosaicTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "messier.csv"),
		[]byte("name,ra,dec,diameter,mag,alias\nM31,10.6847,41.2687,178,3.4,Andromeda Galaxy/NGC224\nM81,148.888,69.065,26.9,6.9,Bode's Galaxy/NGC3031\n"), 0o644))
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

type mosaicPreviewResp struct {
	Query    mosaicQueryEcho   `json:"query"`
	Grid     mosaicplan.Grid   `json:"grid"`
	Tiles    []mosaicplan.Tile `json:"tiles"`
	Warnings []string          `json:"warnings"`
}

func postMosaicPreview(t *testing.T, h http.Handler, body string) (*httptest.ResponseRecorder, mosaicPreviewResp) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mosaic/preview", strings.NewReader(body)))
	var resp mosaicPreviewResp
	if rec.Code == http.StatusOK {
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	}
	return rec, resp
}

func TestMosaicPreview(t *testing.T) {
	h := mosaicTestServer(t).Handler()

	t.Run("M31 with the reference rig plans a 3x2 grid", func(t *testing.T) {
		rec, resp := postMosaicPreview(t, h,
			`{"target_name":"M31","camera_pa_deg":125,"overlap_frac":0.2,"at":"2026-08-15T22:00:00Z"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		assert.Equal(t, 3, resp.Grid.Cols)
		assert.Equal(t, 2, resp.Grid.Rows)
		require.Len(t, resp.Tiles, 6)
		assert.Empty(t, resp.Warnings)
		assert.InDelta(t, 1.37, resp.Query.FovWDeg, 0.01)
		assert.InDelta(t, 1.036, resp.Query.FovHDeg, 0.01)
		assert.InDelta(t, 178, resp.Query.SizeArcmin, 0.5, "size resolved from the catalogue (OpenNGC overlay wins)")
		require.NotNil(t, resp.Query.ObjectPADeg, "OpenNGC posang must flow through")
		assert.InDelta(t, 35, *resp.Query.ObjectPADeg, 1)
		assert.Equal(t, "p01", resp.Tiles[0].Folder)
		assert.NotEmpty(t, resp.Tiles[0].MeridianSide)
	})

	t.Run("explicit coordinates need no catalogue", func(t *testing.T) {
		rec, resp := postMosaicPreview(t, h,
			`{"ra_deg":83.82,"dec_deg":-5.39,"size_arcmin":300,"size_minor_arcmin":180,"object_pa_deg":90,"camera_pa_deg":0}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.GreaterOrEqual(t, resp.Grid.Cols*resp.Grid.Rows, 6)
	})

	t.Run("overlap is clamped, not rejected", func(t *testing.T) {
		rec, resp := postMosaicPreview(t, h, `{"target_name":"M31","overlap_frac":0.9}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.InDelta(t, 0.5, resp.Grid.OverlapFrac, 1e-9)
	})

	t.Run("no target and no coordinates is 400", func(t *testing.T) {
		rec, _ := postMosaicPreview(t, h, `{"camera_pa_deg":10}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unknown target is 400", func(t *testing.T) {
		rec, _ := postMosaicPreview(t, h, `{"target_name":"M999"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid at is 400", func(t *testing.T) {
		rec, _ := postMosaicPreview(t, h, `{"target_name":"M31","at":"tonight"}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestSkyStarfield(t *testing.T) {
	h := mosaicTestServer(t).Handler()

	t.Run("serves stars around the pole", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/starfield?ra=0&dec=90&fov=5&maxmag=9", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var resp struct {
			Stars []struct {
				RADeg  float64 `json:"ra_deg"`
				DecDeg float64 `json:"dec_deg"`
				Mag    float64 `json:"mag"`
			} `json:"stars"`
			Count int `json:"count"`
		}
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.Positive(t, resp.Count, "Polaris and friends live here")
		assert.Equal(t, len(resp.Stars), resp.Count)
		for _, st := range resp.Stars {
			assert.LessOrEqual(t, st.Mag, 9.0)
			assert.Greater(t, st.DecDeg, 86.0, "all stars within 0.75×fov of the pole")
		}
	})

	t.Run("missing fov is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/starfield?ra=10&dec=45", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
