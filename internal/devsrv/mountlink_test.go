package devsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/device/nexstar"
)

// The mount supervisor: what the UI gets to see, and what the server remembers.

func TestServer_MountStatus_ServesTheSameSnapshotAsTheStream(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	res, body := get(t, ts, "/mount")
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, true, body["connected"])
	require.NotNil(t, body["mount"], "a connected mount must report its state")
}

func TestServer_MountStatus_AnswersWhenNothingIsConnected(t *testing.T) {
	ts := testServer(t)
	res, body := get(t, ts, "/mount")
	// Not an error: "no mount" is a state the panel renders, not a failure it has to handle.
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, false, body["connected"])
}

func TestServer_MountEvents_StreamsState(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mount/events", nil)
	require.NoError(t, err)
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))

	// The first frame must arrive immediately rather than after the first tick: a panel that shows
	// nothing for a second on every open reads as broken.
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "data: "))

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload))
	assert.Equal(t, true, payload["connected"])
	assert.NotNil(t, payload["mount"])
}

func TestServer_Diagnose_AnswersWithoutHardware(t *testing.T) {
	ts := testServer(t)
	res, body := get(t, ts, "/diagnose")
	require.Equal(t, http.StatusOK, res.StatusCode)
	// Whatever is or is not plugged into the machine running this, the diagnosis must be well formed
	// — an empty verdict would mean the doctor fell through its own decision table.
	assert.NotEmpty(t, body["verdict"])
	assert.NotEmpty(t, body["detail"])
}

func TestMountLink_RemembersTheLinkThatWorked(t *testing.T) {
	dir := t.TempDir()
	srv := New(&config.Config{WorkDir: dir, DeviceAddr: "127.0.0.1:0"})
	t.Cleanup(srv.Close)

	srv.link.remember("nexstar", "/dev/cu.usbserial-1420", "Advanced VX")

	saved, ok := srv.link.recall()
	require.True(t, ok)
	assert.Equal(t, "/dev/cu.usbserial-1420", saved.Port)
	assert.Equal(t, "nexstar", saved.Driver)
	assert.Positive(t, saved.SavedAt, "timestamps are stored as int64 milliseconds")

	// Deliberately a file next to the scratch space rather than a row in app_settings: this process
	// has no database, which is the whole reason it exists as a separate process.
	_, err := os.Stat(filepath.Join(dir, linkStateFile))
	require.NoError(t, err)
}

func TestMountLink_RemembersNothingWithoutAPort(t *testing.T) {
	dir := t.TempDir()
	srv := New(&config.Config{WorkDir: dir, DeviceAddr: "127.0.0.1:0"})
	t.Cleanup(srv.Close)

	// The simulator is connected with no port at all; remembering an empty one would make the next
	// start try to open "".
	srv.link.remember("sim", "", "Simulated Celestron AVX")
	_, ok := srv.link.recall()
	assert.False(t, ok)
}

func TestMountLink_SuspendKeepsTheHeartbeatOffThePort(t *testing.T) {
	srv := New(&config.Config{DeviceAddr: "127.0.0.1:0"})
	t.Cleanup(srv.Close)

	assert.False(t, srv.link.isSuspended())
	release := srv.link.Suspend()
	assert.True(t, srv.link.isSuspended(), "a run that owns the port must be able to keep the heartbeat away")

	// Releasing twice must not underflow the count — the caller defers it, and a defer that also runs
	// on an early return is exactly how that happens.
	release()
	release()
	assert.False(t, srv.link.isSuspended())
}

func TestMountLink_HealthIsAbsentForADriverThatKeepsNoCounters(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	_, body := get(t, ts, "/mount")
	// The simulator has no serial link, and reporting invented latency for it would make the panel
	// lie in exactly the situation where somebody is trying to tell simulation from hardware.
	assert.Nil(t, body["link"])
}

func TestMountLink_HealthShapeIsWhatThePanelReads(t *testing.T) {
	// A compile-time-ish guard on the JSON contract the frontend binds to: renaming one of these
	// silently empties a field in the panel rather than failing anywhere.
	b, err := json.Marshal(nexstar.LinkHealth{})
	require.NoError(t, err)
	for _, key := range []string{
		"connected", "reconnecting", "path", "uptime_ms", "last_reply_ago_ms",
		"commands", "errors", "retries", "resyncs", "desyncs", "reconnects", "unrecovered",
		"latency_p50_ms", "latency_p99_ms", "latency_max_ms",
	} {
		assert.Contains(t, string(b), `"`+key+`"`)
	}
}
