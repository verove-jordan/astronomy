package devsrv

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// /live/save exists so the engine can plate-solve the very frame the user is watching. Two things have
// to hold for that to work: it must produce a real capture FITS, and it must say WHICH frame it wrote,
// so a caller can refuse one taken before the mount stopped moving.

func TestLiveSave_WritesTheFrameOnScreen(t *testing.T) {
	ts := testServer(t)
	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() { post(t, ts, "/live/stop", nil) })

	waitForLiveFrame(t, ts)

	path := filepath.Join(t.TempDir(), "polar_01.fit")
	resp, body := post(t, ts, "/live/save", map[string]any{
		"path": path, "type": "light", "object": "polar-align",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "%v", body)
	assert.Equal(t, path, body["path"])
	assert.NotNil(t, body["seq"], "the caller needs to know which frame this was")
	assert.NotEmpty(t, body["started_at"], "the solve needs the exposure time to compute an hour angle")

	// It has to be a frame Siril would accept, with the capture header on it.
	f, err := fits.Open(path)
	require.NoError(t, err)
	w, h := f.Dimensions()
	assert.Equal(t, 256, w)
	assert.Equal(t, 256, h)
	obj, _ := f.Header.String("OBJECT")
	assert.Equal(t, "polar-align", obj)
	assert.NotEmpty(t, headerString(t, f, "DATE-OBS"))
}

// The sequence number is what lets the session insist on a frame taken AFTER the user finished turning
// the mount. If it never advanced, a measurement could be built entirely from one stale frame.
func TestLiveSave_SequenceAdvancesWithTheStream(t *testing.T) {
	ts := testServer(t)
	post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	t.Cleanup(func() { post(t, ts, "/live/stop", nil) })
	waitForLiveFrame(t, ts)

	dir := t.TempDir()
	_, first := post(t, ts, "/live/save", map[string]any{"path": filepath.Join(dir, "a.fit")})
	firstSeq, _ := first["seq"].(float64)

	require.Eventually(t, func() bool {
		_, later := post(t, ts, "/live/save", map[string]any{"path": filepath.Join(dir, "b.fit")})
		seq, _ := later["seq"].(float64)
		return seq > firstSeq
	}, 5*time.Second, 20*time.Millisecond, "the live sequence never advanced")
}

func TestLiveSave_RefusesWithNothingToSave(t *testing.T) {
	ts := testServer(t)
	post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})

	resp, body := post(t, ts, "/live/save", map[string]any{"path": filepath.Join(t.TempDir(), "x.fit")})
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "no_frame", body["code"])

	resp, _ = post(t, ts, "/live/save", map[string]any{"path": "  "})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func waitForLiveFrame(t *testing.T, ts *httptest.Server) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(ts.URL + "/live/stats")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK && hasFrame(t, ts)
	}, 10*time.Second, 20*time.Millisecond, "no live frame arrived")
}

func hasFrame(t *testing.T, ts *httptest.Server) bool {
	t.Helper()
	resp, err := http.Get(ts.URL + "/live/frame?max=64")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func headerString(t *testing.T, f *fits.File, key string) string {
	t.Helper()
	v, _ := f.Header.String(key)
	return v
}
