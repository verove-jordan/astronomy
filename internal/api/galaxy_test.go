package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/scene3d"
)

func TestGetGalaxyPoints_ServesTheCloudWithACacheableETag(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.getGalaxyPoints(rec, httptest.NewRequest(http.MethodGet, "/api/galaxy/points", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, galaxyCacheControl, rec.Header().Get("Cache-Control"))
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)
	assert.Equal(t, "ASTROGXY", rec.Body.String()[:8])

	// A conditional re-fetch must cost a 304 and no body — this is a couple of megabytes, and the
	// viewer asks for it once per run it opens.
	req := httptest.NewRequest(http.MethodGet, "/api/galaxy/points", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	s.getGalaxyPoints(rec, req)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
}

func TestGetGalaxyPoints_RefusesAVersionItCannotServe(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.getGalaxyPoints(rec, httptest.NewRequest(http.MethodGet, "/api/galaxy/points?v=99", nil))
	assert.Equal(t, http.StatusConflict, rec.Code,
		"a skew must be a message, not a buffer the viewer will silently reject")
	assert.Contains(t, rec.Body.String(), "galaxy_version_mismatch")

	rec = httptest.NewRecorder()
	s.getGalaxyPoints(rec, httptest.NewRequest(http.MethodGet, "/api/galaxy/points?v=nope", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	s.getGalaxyPoints(rec, httptest.NewRequest(
		http.MethodGet, "/api/galaxy/points?v="+strconv.Itoa(scene3d.GalaxyVersion), nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}
