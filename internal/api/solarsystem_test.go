package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/solarsystem"
)

func solarServer(t *testing.T, workDir string) *Server {
	t.Helper()
	return &Server{cfg: &config.Config{WorkDir: workDir, LatDeg: 48.85, LonDeg: 2.35}}
}

func TestSolarSystemBodies_ServesTheModelWithACacheableETag(t *testing.T) {
	s := solarServer(t, t.TempDir())

	rec := httptest.NewRecorder()
	s.solarSystemBodies(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/bodies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, solarSystemCacheControl, rec.Header().Get("Cache-Control"))
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	var m solarsystem.Manifest
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
	assert.Equal(t, solarsystem.ManifestVersion, m.Version)
	assert.Equal(t, 1800, m.RangeFrom)
	assert.Equal(t, 2050, m.RangeTo)
	assert.NotEmpty(t, m.Bodies)
	assert.NotEmpty(t, m.Sources, "the page has to be able to credit its data")

	req := httptest.NewRequest(http.MethodGet, "/api/solarsystem/bodies", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.solarSystemBodies(rec, req)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

func TestSolarSystemBodies_RefusesAVersionItCannotServe(t *testing.T) {
	s := solarServer(t, t.TempDir())

	rec := httptest.NewRecorder()
	s.solarSystemBodies(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/bodies?v=99", nil))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "solarsystem_version_mismatch")

	rec = httptest.NewRecorder()
	s.solarSystemBodies(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/bodies?v=nope", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	s.solarSystemBodies(rec, httptest.NewRequest(http.MethodGet,
		"/api/solarsystem/bodies?v="+strconv.Itoa(solarsystem.ManifestVersion), nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestSolarSystemState(t *testing.T) {
	s := solarServer(t, t.TempDir())

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"defaults to now", "", http.StatusOK},
		{"an explicit instant", "?t=1755043200000", http.StatusOK},
		{"a site of its own", "?lat=-33.9&lon=18.4", http.StatusOK},
		{"a timestamp that is not a number", "?t=yesterday", http.StatusBadRequest},
		{"a year the model does not cover", "?t=-6000000000000", http.StatusBadRequest},
		{"a latitude off the Earth", "?lat=120", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.solarSystemState(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/state"+tt.query, nil))
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

// TestSolarSystemState_AgreesWithTheModel checks the handler is a thin wrapper and not a second
// opinion: what it serves for an instant is what the package computes for that instant.
func TestSolarSystemState_AgreesWithTheModel(t *testing.T) {
	s := solarServer(t, t.TempDir())
	when := time.Date(2026, 8, 13, 21, 30, 0, 0, time.UTC)

	rec := httptest.NewRecorder()
	s.solarSystemState(rec, httptest.NewRequest(http.MethodGet,
		"/api/solarsystem/state?t="+strconv.FormatInt(when.UnixMilli(), 10), nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got solarsystem.Snapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	want := solarsystem.StateAt(when, solarsystem.Site{LatDeg: 48.85, LonDeg: 2.35})

	require.Equal(t, len(want.Bodies), len(got.Bodies))
	assert.Equal(t, when.UnixMilli(), got.TimeMs)
	for i := range want.Bodies {
		assert.Equal(t, want.Bodies[i].Key, got.Bodies[i].Key)
		assert.InDelta(t, want.Bodies[i].RADeg, got.Bodies[i].RADeg, 1e-9)
		assert.InDelta(t, want.Bodies[i].AltDeg, got.Bodies[i].AltDeg, 1e-9)
	}
}

func TestSolarSystemTexture(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, solarsystem.TextureDirName), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, solarsystem.TextureDirName, "mars.jpg"), []byte("not really a jpeg"), 0o644))
	s := solarServer(t, dir)

	rec := httptest.NewRecorder()
	s.solarSystemTexture(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/texture?key=mars", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "not really a jpeg", rec.Body.String())
	assert.Equal(t, solarTextureCacheControl, rec.Header().Get("Cache-Control"))

	// An absent texture is ordinary — the page shades that body procedurally instead.
	rec = httptest.NewRecorder()
	s.solarSystemTexture(rec, httptest.NewRequest(http.MethodGet, "/api/solarsystem/texture?key=venus", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "texture_not_downloaded")

	// A key is a bare word, so no request can reach outside the texture directory.
	for _, key := range []string{"../../../etc/passwd", "..", "mars.jpg", ""} {
		rec = httptest.NewRecorder()
		s.solarSystemTexture(rec, httptest.NewRequest(http.MethodGet,
			"/api/solarsystem/texture?key="+key, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, "key %q must not resolve", key)
	}
}
