package devsrv

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// testServer builds a device server against a small simulated sensor, so a whole capture round trip
// costs milliseconds.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := &config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 256, SensorHpx: 256,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35,
		DeviceAddr: "127.0.0.1:0",
	}
	srv := New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts
}

func post(t *testing.T, ts *httptest.Server, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	} else {
		buf.WriteString("{}")
	}
	resp, err := http.Post(ts.URL+path, "application/json", &buf)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func get(t *testing.T, ts *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestServer_HealthReportsTheSimulator(t *testing.T) {
	ts := testServer(t)
	resp, body := get(t, ts, "/health")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["ok"])

	drivers, _ := body["drivers"].([]any)
	require.NotEmpty(t, drivers, "the simulator must always be offered")
	first, _ := drivers[0].(map[string]any)
	assert.Equal(t, "sim", first["name"])
	assert.Equal(t, true, first["available"])
}

func TestServer_RefusesWorkBeforeConnecting(t *testing.T) {
	ts := testServer(t)
	resp, body := post(t, ts, "/camera/expose", map[string]any{})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "not_connected", body["code"],
		"a disconnected camera is a normal state the UI shows, not a server error")
}

// The end-to-end capture contract: connect, configure, expose, save — and the file that lands must
// be classified correctly by the same scanner the processing pipeline uses.
func TestServer_CaptureWritesAnInspectableFrame(t *testing.T) {
	ts := testServer(t)
	dir := t.TempDir()

	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/wheel/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/camera/control",
		map[string]any{"name": "exposure", "value": 120_000})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/camera/control", map[string]any{"name": "gain", "value": 139})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Ha is slot 5 on the simulated wheel; wait for it so the frame is not shot mid-move.
	resp, _ = post(t, ts, "/wheel/position", map[string]any{"slot": 5, "wait": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/camera/expose", map[string]any{})
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	path := filepath.Join(dir, "p01", "Light_0.12sec_Bin1_filter-Ha_gain139_frame0001.fit")
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, _ = post(t, ts, "/camera/save", map[string]any{
			"path": path, "type": "light", "filter": "Ha", "object": "M31",
			"focal_mm": 740, "panel": "p01", "session_id": "test",
		})
		if resp.StatusCode == http.StatusOK {
			break
		}
		require.True(t, time.Now().Before(deadline), "save never succeeded")
		time.Sleep(20 * time.Millisecond)
	}

	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 1)
	fr := inv.Frames[0]
	assert.Equal(t, inspect.Light, fr.Type)
	assert.Equal(t, "Ha", fr.Filter)
	assert.Equal(t, int64(139), fr.Gain)
	assert.Equal(t, "M31", fr.Object)
	assert.Equal(t, "astrostack", fr.Creator)
	assert.NotEmpty(t, fr.Session, "a capture must carry a parseable night key")

	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Positive(t, st.Size())
}

func TestServer_MountSafetyRefusesTargetsBelowTheHorizon(t *testing.T) {
	ts := testServer(t)
	resp, _ := post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The south celestial pole is permanently below the horizon from the configured northern site.
	resp, body := post(t, ts, "/mount/goto", map[string]any{"ra_deg": 0.0, "dec_deg": -89.0})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, body["error"], "horizon")

	// …and force overrides it, for the deliberate cases.
	resp, _ = post(t, ts, "/mount/goto",
		map[string]any{"ra_deg": 0.0, "dec_deg": -89.0, "force": true})
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

func TestServer_MountAbortAlwaysAnswers(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/mount/goto", map[string]any{"ra_deg": 200.0, "dec_deg": 40.0})

	resp, body := post(t, ts, "/mount/abort", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["ok"], "STOP must work with no arguments and no preconditions")
}

func TestServer_LiveViewServesFramesAndStats(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/camera/control", map[string]any{"name": "exposure", "value": 30_000})

	resp, _ := post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() { _, _ = post(t, ts, "/live/stop", nil) })

	// Wait for the first frame to land.
	deadline := time.Now().Add(5 * time.Second)
	var stats map[string]any
	for {
		_, body := get(t, ts, "/live/stats")
		if s, ok := body["stats"].(map[string]any); ok {
			stats = s
			break
		}
		require.True(t, time.Now().Before(deadline), "no live frame arrived")
		time.Sleep(25 * time.Millisecond)
	}
	assert.Equal(t, float64(256), stats["width"])
	hist, _ := stats["histogram"].([]any)
	assert.Len(t, hist, histogramBins)
	assert.Greater(t, stats["mean"], 0.0)

	// The frame endpoint serves the engine's binary preview format, which the browser already
	// knows how to decode: [w u32][h u32][c u32][lo u16][hi u16] then the samples.
	resp, err := http.Get(ts.URL + "/live/frame?max=128")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	buf := make([]byte, 16)
	n, _ := resp.Body.Read(buf)
	assert.Equal(t, 16, n, "the preview header must be present")
	assert.NotEmpty(t, resp.Header.Get("X-Frame-Seq"))
}

func TestServer_LiveSimulateDefocusesTheTelescope(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})

	resp, body := post(t, ts, "/live/simulate", map[string]any{"focus_offset_um": 250.0})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.InDelta(t, 250, body["focus_offset_um"], 1e-6)
}

func TestServer_SaveRequiresAPath(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	resp, body := post(t, ts, "/camera/save", map[string]any{"type": "light"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.True(t, strings.Contains(body["error"].(string), "path"))
}
