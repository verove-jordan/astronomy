package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/config"
)

// TestLibraryS3Sync_Validation covers the handler's guards, which all return before touching the store/mgr:
// a malformed body, a missing bucket, and S3 not being configured each 400 (never a panic on the partial
// Server). The happy path (enqueue + record mirror location) needs a live manager + Postgres, exercised by
// the transfer/job machinery elsewhere.
func TestLibraryS3Sync_Validation(t *testing.T) {
	h := (&Server{cfg: &config.Config{LibraryDir: t.TempDir()}}).Handler()
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/library/s3-sync", strings.NewReader(body)))
		return rec.Code
	}
	assert.Equal(t, http.StatusBadRequest, post("not json"), "malformed body")
	assert.Equal(t, http.StatusBadRequest, post(`{}`), "missing bucket")
	assert.Equal(t, http.StatusBadRequest, post(`{"bucket":"b","prefix":"p"}`), "S3 not configured (no creds)")
}
