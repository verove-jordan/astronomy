package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGzipJSON_CompressesJSONOnly(t *testing.T) {
	big := strings.Repeat("x", 4096)
	cases := []struct {
		name, ctype string
		wantGzip    bool
	}{
		{"json compressed", "application/json", true},
		{"png passthrough", "image/png", false},         // weather/LP tiles must stay uncompressed
		{"sse passthrough", "text/event-stream", false}, // SSE streams must not be buffered/compressed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := gzipJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", c.ctype)
				_, _ = io.WriteString(w, big)
			}))
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if c.wantGzip {
				require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
				zr, err := gzip.NewReader(rec.Body)
				require.NoError(t, err)
				body, _ := io.ReadAll(zr)
				assert.Equal(t, big, string(body), "gzip round-trips losslessly")
			} else {
				assert.NotEqual(t, "gzip", rec.Header().Get("Content-Encoding"))
				assert.Equal(t, big, rec.Body.String())
			}
		})
	}
}

func TestGzipJSON_SkipsWithoutAcceptEncoding(t *testing.T) {
	h := gzipJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("y", 4096))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil)) // no Accept-Encoding
	assert.NotEqual(t, "gzip", rec.Header().Get("Content-Encoding"))
}

func TestWriteJSONCached_304(t *testing.T) {
	const etag = `W/"v1-123"`
	// No If-None-Match → 200 with ETag + Cache-Control + body.
	rec := httptest.NewRecorder()
	writeJSONCached(rec, httptest.NewRequest("GET", "/x", nil), http.StatusOK, etag, "private, max-age=60", map[string]int{"a": 1})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, etag, rec.Header().Get("ETag"))
	assert.Equal(t, "private, max-age=60", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), `"a":1`)

	// Matching If-None-Match → 304, empty body.
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	writeJSONCached(rec2, req, http.StatusOK, etag, "private, max-age=60", map[string]int{"a": 1})
	assert.Equal(t, http.StatusNotModified, rec2.Code)
	assert.Empty(t, rec2.Body.String())

	// Non-matching → 200 with body.
	req3 := httptest.NewRequest("GET", "/x", nil)
	req3.Header.Set("If-None-Match", `W/"other"`)
	rec3 := httptest.NewRecorder()
	writeJSONCached(rec3, req3, http.StatusOK, etag, "", map[string]int{"a": 1})
	assert.Equal(t, http.StatusOK, rec3.Code)
}

func TestEtagMatches(t *testing.T) {
	assert.True(t, etagMatches(`W/"v1"`, `W/"v1"`))
	assert.True(t, etagMatches(`"v1"`, `W/"v1"`))        // weak vs strong prefix ignored
	assert.True(t, etagMatches(`W/"a", W/"v1"`, `"v1"`)) // comma list
	assert.True(t, etagMatches(`*`, `W/"v1"`))
	assert.False(t, etagMatches(`W/"other"`, `W/"v1"`))
	assert.False(t, etagMatches("", `W/"v1"`))
}
