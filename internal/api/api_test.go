package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

func TestWithin(t *testing.T) {
	s := &Server{cfg: &config.Config{DataDir: "/tmp/data"}}
	cases := []struct {
		path string
		ok   bool
	}{
		{"/tmp/data", true},
		{"/tmp/data/M31", true},
		{"/tmp/data/../data/M31", true},
		{"/tmp/dataother", false},
		{"/etc/passwd", false},
		{"", false},
	}
	for _, tc := range cases {
		_, ok := s.within(tc.path, "/tmp/data")
		assert.Equal(t, tc.ok, ok, tc.path)
	}
}

func TestServeFile_TraversalBlocked(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("hello"), 0o644))
	s := &Server{cfg: &config.Config{OutputDir: dir}}
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file?path="+filepath.Join(dir, "ok.txt"), nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/file?path=/etc/passwd", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCORSPreflight(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/jobs", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
}
