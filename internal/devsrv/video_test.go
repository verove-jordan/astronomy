package devsrv

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/ser"
)

// videoServer builds a bare device server (no HTTP) with the simulated camera connected — the
// recorder is exercised directly, so the assertions are about the file rather than JSON plumbing.
func videoServer(t *testing.T, connect bool) *Server {
	t.Helper()
	srv := New(&config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 64, SensorHpx: 48,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35, DeviceAddr: "127.0.0.1:0",
	})
	t.Cleanup(srv.Close)
	if connect {
		cam, err := srv.openCamera("sim")
		require.NoError(t, err)
		require.NoError(t, cam.Connect(context.Background()))
		srv.mu.Lock()
		srv.camera = cam
		srv.mu.Unlock()
	}
	return srv
}

// Recording end to end: the simulated camera streams, frames land in a SER file, and the file that
// comes out is one a planetary stacker could actually open. Asserting the on-disk bytes rather than
// the recorder's own counters is the point — a counter can be right while the file is empty.
func TestVideoRecorder_WritesAReadableFile(t *testing.T) {
	srv := videoServer(t, true)

	path := filepath.Join(t.TempDir(), "mars")
	require.NoError(t, srv.video.Start(VideoOptions{
		Path: path, ExposureUs: 20_000, Gain: 200, MaxFrames: 6,
		Object: "Mars", Telescope: "FC-100 DF",
	}))

	// The recorder stops itself at MaxFrames; Stop also covers the case where it already has.
	st := waitForRecording(t, srv)
	assert.False(t, st.Running)
	assert.GreaterOrEqual(t, st.Frames, 6)

	// ".ser" must have been appended — planetary ingest keys on the extension.
	written := path + ".ser"
	b, err := os.ReadFile(written)
	require.NoError(t, err, "the recording must exist at the extension-completed path")

	assert.Equal(t, "LUCAM-RECORDER", string(b[0:14]))
	frames := int(binary.LittleEndian.Uint32(b[38:42]))
	assert.Equal(t, st.Frames, frames, "the header's frame count must match what was recorded")

	w := int(binary.LittleEndian.Uint32(b[26:30]))
	h := int(binary.LittleEndian.Uint32(b[30:34]))
	assert.Positive(t, w)
	assert.Positive(t, h)
	assert.Equal(t, ser.HeaderSize+frames*w*h*2+frames*8, len(b),
		"the file size must be exactly header + frames + timestamps")

	assert.Equal(t, "Mars", trimField(b[42:82]), "the object is recorded as the observer field")
	assert.Equal(t, "FC-100 DF", trimField(b[122:162]))

	// Timestamps must be real dates, which is what proves the epoch arithmetic survived the trip.
	first := ser.FromSerTime(binary.LittleEndian.Uint64(b[ser.HeaderSize+frames*w*h*2:]))
	assert.Greater(t, first.Year(), 2020, "a saturated epoch conversion would give year 1 or 292")
	assert.Less(t, first.Year(), 2100)
}

// A recording and the live view both drive the one camera; starting a recording must take it over
// rather than let the two fight and drop frames from both.
func TestVideoRecorder_StopsTheLiveView(t *testing.T) {
	srv := videoServer(t, true)

	require.NoError(t, srv.live.start(200*time.Millisecond))
	require.True(t, srv.live.isRunning())

	path := filepath.Join(t.TempDir(), "v.ser")
	require.NoError(t, srv.videoStartForTest(VideoOptions{Path: path, MaxFrames: 2, ExposureUs: 20_000}))
	assert.False(t, srv.live.isRunning(), "the live view must yield the camera to the recording")
	_ = srv.video.Stop()
}

// Two recordings at once would interleave frames into one file.
func TestVideoRecorder_RefusesASecondRecording(t *testing.T) {
	srv := videoServer(t, true)

	dir := t.TempDir()
	require.NoError(t, srv.video.Start(VideoOptions{
		Path: filepath.Join(dir, "a.ser"), ExposureUs: 100_000, MaxSeconds: 30,
	}))
	defer func() { _ = srv.video.Stop() }()

	err := srv.video.Start(VideoOptions{Path: filepath.Join(dir, "b.ser"), ExposureUs: 100_000})
	assert.ErrorContains(t, err, "already running")
}

// Stopping mid-stream must still leave a complete, readable file — an aborted planetary run is
// normal (the seeing collapses, cloud arrives) and those frames are still worth stacking.
func TestVideoRecorder_StopMidStreamLeavesAValidFile(t *testing.T) {
	srv := videoServer(t, true)

	path := filepath.Join(t.TempDir(), "v.ser")
	require.NoError(t, srv.video.Start(VideoOptions{
		Path: path, ExposureUs: 20_000, MaxSeconds: 60, // far longer than the test will run
	}))
	// Let a few frames accumulate, then interrupt.
	deadline := time.Now().Add(5 * time.Second)
	for srv.video.Status().Frames < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	st := srv.video.Stop()

	assert.False(t, st.Running)
	assert.GreaterOrEqual(t, st.Frames, 3)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(st.Frames), binary.LittleEndian.Uint32(b[38:42]),
		"an interrupted recording must still have its true frame count written")
}

func TestVideoRecorder_NeedsACamera(t *testing.T) {
	srv := videoServer(t, false)
	err := srv.video.Start(VideoOptions{Path: filepath.Join(t.TempDir(), "v.ser")})
	assert.Error(t, err, "recording without a camera must fail rather than create an empty file")
}

func TestVideoRecorder_NeedsAPath(t *testing.T) {
	srv := videoServer(t, true)
	assert.ErrorContains(t, srv.video.Start(VideoOptions{}), "path")
}

// videoStartForTest mirrors the HTTP handler's ordering (stop live, then record) without the
// request plumbing.
func (s *Server) videoStartForTest(opts VideoOptions) error {
	s.live.stop()
	return s.video.Start(opts)
}

// waitForRecording blocks until the recorder finishes on its own.
func waitForRecording(t *testing.T, srv *Server) VideoStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st := srv.video.Status(); !st.Running && st.Finished {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the recording never finished")
	return VideoStatus{}
}

// trimField reads a space-padded fixed-width header field.
func trimField(b []byte) string {
	end := len(b)
	for end > 0 && b[end-1] == ' ' {
		end--
	}
	return string(b[:end])
}
