package devsrv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Recording the live view: the frames are already being taken, so keeping them must cost nothing but
// a disk write — no mode change, no restart, and starting from the picture that prompted the click.

// recordingServer starts a live view over the simulator, at a frame rate fast enough that the cap is
// what limits the recording rather than the camera.
func recordingServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := testServer(t)
	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/camera/control", map[string]any{"name": "exposure", "value": 1000})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 1})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return ts
}

func recordStatus(t *testing.T, ts *httptest.Server) LiveRecordStatus {
	t.Helper()
	resp, err := http.Get(ts.URL + "/live/record")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	var st LiveRecordStatus
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&st))
	return st
}

func TestLiveRecorder_SavesFramesTheLiveViewIsAlreadyTaking(t *testing.T) {
	ts := recordingServer(t)
	dir := t.TempDir()

	resp, _ := post(t, ts, "/live/record/start", map[string]any{
		"dir": dir, "max_frames": 5, "filter": "L", "object": "NGC7000",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool { return !recordStatus(t, ts).Running },
		10*time.Second, 20*time.Millisecond, "the recording must stop at max_frames")

	st := recordStatus(t, ts)
	assert.Equal(t, 5, st.Saved)
	assert.Empty(t, st.LastError)

	// The files must be the ones the rest of the pipeline reads, classified from the name alone.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 5)
	for _, e := range entries {
		assert.True(t, strings.HasPrefix(e.Name(), "Light_"), "got %q", e.Name())
		assert.True(t, strings.HasSuffix(e.Name(), ".fit"), "got %q", e.Name())
	}
	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, 5, len(inv.Frames), "inspect must recognise every frame")
}

// The cap is a BURST, not a comb: the frames kept in one second are consecutive. Evenly spaced ones
// would be the least useful, because seeing decorrelates between them.
func TestLiveRecorder_KeepsConsecutiveFramesRatherThanEveryNth(t *testing.T) {
	ts := recordingServer(t)
	dir := t.TempDir()

	// One frame a second of budget, so anything after the first in each second is skipped — which
	// makes the arithmetic visible: with a much faster live loop, skipped must dwarf saved.
	resp, _ := post(t, ts, "/live/record/start", map[string]any{
		"dir": dir, "max_fps": 1, "max_frames": 2,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Eventually(t, func() bool { return !recordStatus(t, ts).Running },
		10*time.Second, 20*time.Millisecond)

	st := recordStatus(t, ts)
	assert.Equal(t, 2, st.Saved)
	assert.Greater(t, st.Skipped, 0,
		"a live loop faster than the cap must report the frames the cap dropped")
}

func TestLiveRecorder_StopIsIdempotentSoTheButtonCanBeAToggle(t *testing.T) {
	ts := recordingServer(t)

	resp, _ := post(t, ts, "/live/record/start", map[string]any{"dir": t.TempDir(), "max_frames": 500})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.True(t, recordStatus(t, ts).Running)

	for i := 0; i < 2; i++ {
		resp, _ := post(t, ts, "/live/record/stop", nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "stopping twice is a double click, not a fault")
	}
	require.Eventually(t, func() bool { return !recordStatus(t, ts).Running },
		5*time.Second, 20*time.Millisecond)
}

func TestLiveRecorder_RefusesToRecordAStoppedLiveView(t *testing.T) {
	ts := testServer(t)
	resp, body := post(t, ts, "/live/record/start", map[string]any{"dir": t.TempDir()})

	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "not_connected", body["code"],
		"there is nothing to record, which the panel shows rather than treating as an error")
}

func TestLiveRecorder_RefusesASecondRecording(t *testing.T) {
	ts := recordingServer(t)
	resp, _ := post(t, ts, "/live/record/start", map[string]any{"dir": t.TempDir(), "max_frames": 500})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, body := post(t, ts, "/live/record/start", map[string]any{"dir": t.TempDir()})
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Equal(t, "busy", body["code"])
}

// Nobody names the filter when they press a button, so the recorder asks the wheel. Without it every
// frame was nameless — no FILTER card and no filter- token — which for a mono rig is the one piece
// of metadata the stack cannot be assembled without.
func TestLiveRecorder_TakesTheFilterFromTheWheel(t *testing.T) {
	ts := recordingServer(t)
	resp, _ := post(t, ts, "/wheel/connect", map[string]any{
		"driver": "sim", "names": []string{"L", "R", "G", "B", "Ha"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/wheel/position", map[string]any{"slot": 2, "wait": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	dir := t.TempDir()
	resp, _ = post(t, ts, "/live/record/start", map[string]any{"dir": dir, "max_frames": 1})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return !recordStatus(t, ts).Running },
		10*time.Second, 20*time.Millisecond)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "filter-R", "slot 2 is R; got %q", entries[0].Name())

	inv, err := inspect.Scan(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, inv.Frames, 1)
	assert.Equal(t, "R", inv.Frames[0].Filter)
}

// An explicit filter always wins over the wheel, so a caller that knows better is not overruled.
func TestLiveRecorder_AnExplicitFilterWinsOverTheWheel(t *testing.T) {
	ts := recordingServer(t)
	resp, _ := post(t, ts, "/wheel/connect", map[string]any{
		"driver": "sim", "names": []string{"L", "R", "G", "B", "Ha"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	dir := t.TempDir()
	resp, _ = post(t, ts, "/live/record/start", map[string]any{
		"dir": dir, "max_frames": 1, "filter": "OIII",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return !recordStatus(t, ts).Running },
		10*time.Second, 20*time.Millisecond)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), "filter-OIII")
}
