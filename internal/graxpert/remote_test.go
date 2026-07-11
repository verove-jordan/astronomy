package graxpert

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunRemote_StreamsProgressThenResult: in offload mode the runner POSTs to the host service, forwards
// its GraXpert progress lines through onProgress, and treats the trailing "ok" sentinel as success.
func TestRunRemote_StreamsProgressThenResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f := w.(http.Flusher)
		fmt.Fprintln(w, "Progress: 50%")
		f.Flush()
		fmt.Fprintln(w, "Progress: 100%")
		f.Flush()
		fmt.Fprintln(w, ResultPrefix+"ok")
		f.Flush()
	}))
	defer srv.Close()

	var lines []string
	err := New("", srv.URL).Denoise(context.Background(), "/shared/in.fits", "/shared/out.fits",
		DenoiseOptions{}, func(p Progress) {
			if p.Line != "" {
				lines = append(lines, p.Line)
			}
		})
	require.NoError(t, err)
	assert.Equal(t, []string{"Progress: 50%", "Progress: 100%"}, lines, "GraXpert lines forwarded")
}

// TestRunRemote_ErrorSentinel: the host service's "error:<msg>" sentinel surfaces as a failed call.
func TestRunRemote_ErrorSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, ResultPrefix+"error:the model exploded")
	}))
	defer srv.Close()

	err := New("", srv.URL).ExtractBackground(context.Background(), "/a", "/b", BackgroundOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the model exploded")
}

// TestRunRemote_TruncatedStream: a connection that closes before the sentinel is an error (the host died
// mid-run), not a silent success.
func TestRunRemote_TruncatedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "Progress: 10%") // no result sentinel → truncated
	}))
	defer srv.Close()

	err := New("", srv.URL).Denoise(context.Background(), "/a", "/b", DenoiseOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before the run completed")
}
