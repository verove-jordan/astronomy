package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The manage-download Content-Disposition must be RFC 6266-compliant: non-ASCII object names ship as the
// extended, percent-encoded filename* parameter — never as raw UTF-8 (or quote-breaking bytes) inside a
// hand-built quoted string.
func TestWriteAttachmentHeaders(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		size        int64
		wantDispo   string
		wantNoQuote bool // non-ASCII names must not fall back to a raw quoted string
		wantLength  string
	}{
		{
			name:        "utf-8 filename uses extended parameter",
			key:         "M31 été/reçu.fits",
			size:        42,
			wantDispo:   "filename*=utf-8''re%C3%A7u.fits",
			wantNoQuote: true,
			wantLength:  "42",
		},
		{
			name:       "plain ascii filename stays simple",
			key:        "data/M101/final.png",
			size:       7,
			wantDispo:  "filename=final.png",
			wantLength: "7",
		},
		{
			name:      "unknown size omits Content-Length",
			key:       "x/plain.fits",
			size:      -1,
			wantDispo: "filename=plain.fits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeAttachmentHeaders(w, tt.key, tt.size)
			})
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/s3/manage/download", nil))

			dispo := rec.Header().Get("Content-Disposition")
			assert.Contains(t, dispo, "attachment")
			assert.Contains(t, dispo, tt.wantDispo)
			if tt.wantNoQuote {
				assert.NotContains(t, dispo, `"`, "no raw quoted string for a non-ASCII name")
			}
			assert.Equal(t, tt.wantLength, rec.Header().Get("Content-Length"))
			assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
		})
	}
}
