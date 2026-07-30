package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/config"
)

func TestJobRunDir(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty result", "", ""},
		{"pipeline run", `{"output_dir":"/out/M101/run-1"}`, "/out/M101/run-1"},
		{"planetary out_base", `{"out_base":"/out/Moon/run-2/Moon_stack"}`, "/out/Moon/run-2"},
		{"output_dir wins over out_base", `{"output_dir":"/a","out_base":"/b/x"}`, "/a"},
		{"malformed json", `{`, ""},
		{"neither field", `{"object":"M101"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, jobRunDir([]byte(tt.raw)))
		})
	}
}

func TestStars_InvalidJobID(t *testing.T) {
	// The id parse fails before any store access, so the bare Server literal is enough here (the
	// store-dependent paths are exercised by the annotate package tests + the live E2E).
	s := &Server{cfg: &config.Config{}}
	h := s.Handler()

	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/jobs/not-a-number/stars", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, method)
		assert.Contains(t, rec.Body.String(), "invalid_job_id", method)
	}
}
