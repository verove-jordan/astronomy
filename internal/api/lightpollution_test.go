package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

func lpTestServer(t *testing.T, withProvider bool) *Server {
	t.Helper()
	cfg := &config.Config{WorkDir: t.TempDir(), DataDir: t.TempDir(), LatDeg: 48.86, LonDeg: 2.35, SkyDefaultSQM: 21.3}
	s := &Server{cfg: cfg}
	if withProvider {
		s.lightpollution = lightpollution.New(cfg)
	}
	return s
}

func TestLightPollution_PointFallsBackToDefault(t *testing.T) {
	h := lpTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution?lat=48.86&lon=2.35", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Site    lightpollution.SiteQuality `json:"site"`
		Warning string                     `json:"warning"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.InDelta(t, 21.3, resp.Site.SQM, 0.001)
	assert.Equal(t, 4, resp.Site.Bortle)
	assert.Equal(t, "default", resp.Site.Source)
	assert.NotEmpty(t, resp.Warning)
}

func TestLightPollution_OutOfRange(t *testing.T) {
	h := lpTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution?lat=200", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestLightPollutionTile_TransparentWhenNoSource(t *testing.T) {
	h := lpTestServer(t, true).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution/tiles/5/15/10", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.NotEmpty(t, rec.Body.Bytes())
}

func TestLightPollution_NilProviderIsSafe(t *testing.T) {
	h := lpTestServer(t, false).Handler() // partial server, no provider
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution?lat=48&lon=2", nil))
	require.Equal(t, http.StatusOK, rec.Code) // siteAt nil-guard → zero site, never panics
}

// TestSkyTargets_FoldsInLightPollution proves the end-to-end wiring: GET /api/sky/targets resolves the
// site brightness, surfaces it, AND folds it into each target's score. The default provider (no API/
// atlas configured) resolves to the Bortle-4 default, which mildly penalizes a faint galaxy.
func TestSkyTargets_FoldsInLightPollution(t *testing.T) {
	srv := skyTestServer(t) // catalog (M81 galaxy + NGC104) + config, no provider yet
	srv.cfg.SkyDefaultSQM = 21.3
	srv.lightpollution = lightpollution.New(srv.cfg) // no API/atlas → resolves to the default site
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/targets?at=2026-03-15T23:00:00Z", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp skyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "default", resp.Site.Source)
	assert.Equal(t, 4, resp.Site.Bortle)
	assert.InDelta(t, 21.3, resp.Site.SQM, 0.001)

	var m81 *skyplan.Target
	for i := range resp.Targets {
		if resp.Targets[i].Name == "M81" {
			m81 = &resp.Targets[i]
		}
	}
	require.NotNil(t, m81)
	// Under a Bortle-4 sky the faint galaxy takes a real but mild light-pollution penalty.
	assert.Greater(t, m81.SubScores.LightPollution, 0.0)
	assert.Less(t, m81.SubScores.LightPollution, 1.0)
}

// solidTilePNG encodes an opaque solid-color PNG (stand-in for a GIBS night-lights tile).
func solidTilePNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func lpTileServer(t *testing.T, h http.HandlerFunc) *Server {
	t.Helper()
	upstream := httptest.NewServer(h)
	t.Cleanup(upstream.Close)
	cfg := &config.Config{
		WorkDir: t.TempDir(), DataDir: t.TempDir(),
		LightPollutionTileURL: upstream.URL + "/{z}/{x}/{y}.png",
	}
	return &Server{cfg: cfg, lightpollution: lightpollution.New(cfg)}
}

func TestLightPollutionTile_SuccessColoredAndLongCache(t *testing.T) {
	black := solidTilePNG(t, color.RGBA{0, 0, 0, 0xff})
	h := lpTileServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(black)
	}).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution/tiles/5/15/10", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=604800", rec.Header().Get("Cache-Control"))
	img, err := png.Decode(rec.Body)
	require.NoError(t, err)
	assert.Equal(t, 8, img.Bounds().Dx(), "the rendered 8×8 colored tile, not the 1×1 transparent placeholder")
	_, _, _, a := img.At(0, 0).RGBA()
	assert.Equal(t, uint32(0xffff), a, "colored pixel is opaque")
}

func TestLightPollutionTile_TransientFailureSurfacesError(t *testing.T) {
	h := lpTileServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sky/lightpollution/tiles/5/15/10", nil))
	// A transient upstream failure must surface as a 5xx (not a blank 200) so the map's tileerror retry
	// kicks in — a blank tile reads as success and would leave a permanent hole in the overlay.
	require.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "transient failure must not be cached")
}
