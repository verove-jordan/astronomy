package devsrv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// The focus meter against a telescope whose defocus is KNOWN: the simulator is told to sit N µm off
// focus, and the meter has to find its way back. This is the closest thing to a real focusing run
// that can be written without a telescope, and it is what makes the feature trustworthy before any
// hardware exists.

func focusServer(t *testing.T) *httptest.Server {
	t.Helper()
	// The real rig's plate scale (740 mm, 3.8 µm) so the optics maths is the production one, on a
	// small sensor so a frame is cheap. A few arcminutes of real sky can hold no catalogue stars at
	// all, so the test plants its own (below) rather than hoping.
	cfg := &config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 512, SensorHpx: 512,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35,
	}
	srv := New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts
}

// liveFocus runs the live loop until a reliable focus reading lands, and returns it.
// focusAfterSettling waits until the meter's smoothing window holds only frames taken since the
// focuser moved.
//
// The meter deliberately averages over several frames (seeing makes single readings jump around),
// so immediately after a focus change its history is a mix of old and new and the advice describes
// the transition rather than the new position. The wait counts NEW FRAMES via the stream's sequence
// number — not elapsed time, and not repeated reads of the same frame, both of which silently pass
// on a fast machine and mislead on a slow one.
func focusAfterSettling(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	// Twice the advice window, so both sides of the comparison are post-change readings.
	waitForNewFrames(t, ts, 2*focusAdviceWindow+2)
	return liveFocus(t, ts)
}

// focusAdviceWindow mirrors internal/focus's window; if that changes, this wait must too.
const focusAdviceWindow = 3

// waitForNewFrames blocks until the live stream has produced n frames beyond the current one.
func waitForNewFrames(t *testing.T, ts *httptest.Server, n int64) {
	t.Helper()
	start := liveSeq(t, ts)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if liveSeq(t, ts) >= start+n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the live view produced fewer than %d new frames in 60 s", n)
}

// liveSeq reads the stream's frame counter.
func liveSeq(t *testing.T, ts *httptest.Server) int64 {
	t.Helper()
	resp, err := http.Get(ts.URL + "/live/stats")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Seq int64 `json:"seq"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body.Seq
}

func liveFocus(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(ts.URL + "/live/stats")
		require.NoError(t, err)
		var body struct {
			Stats struct {
				Focus map[string]any `json:"focus"`
			} `json:"stats"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if f := body.Stats.Focus; f != nil {
			if ok, _ := f["reliable"].(bool); ok {
				return f
			}
		}
		require.True(t, time.Now().Before(deadline), "no reliable focus reading arrived")
		time.Sleep(100 * time.Millisecond)
	}
}

func TestFocus_ScoreTracksTheSimulatedDefocus(t *testing.T) {
	ts := focusServer(t)
	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, _ = post(t, ts, "/camera/control", map[string]any{"name": "exposure", "value": 500_000})

	// A clean sensor with a known star field: hot pixels are simulated by default (every real
	// sensor has them) but this test is about the optics, and the defect rejection has its own test.
	_, _ = post(t, ts, "/live/simulate", map[string]any{
		"focus_offset_um": 0.0,
		"hot_pixels":      0,
		// A KNOWN field: the planted grid and nothing else. The synthetic faint population makes the
		// live view realistic, but here it would change what the meter averages over between focus
		// positions (faint stars vanish when defocused), which is not what this test measures.
		"faint_stars_per_deg2": -1.0,
		"star_grid":            map[string]any{"count": 4, "mag": 7.0, "spread_deg": 0.1},
	})
	resp, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() { _, _ = post(t, ts, "/live/stop", nil) })

	sharp := liveFocus(t, ts)
	sharpHFD := sharp["hfd_px"].(float64)
	require.Positive(t, sharpHFD)
	assert.Positive(t, sharp["stars"])

	// Now push the focuser 300 µm out and let a few frames go by.
	_, _ = post(t, ts, "/live/simulate", map[string]any{"focus_offset_um": 300.0})
	soft := focusAfterSettling(t, ts)
	softHFD := soft["hfd_px"].(float64)

	assert.Greater(t, softHFD, sharpHFD*1.5,
		"300 µm of defocus must show up as a clearly larger HFD")
	assert.Less(t, soft["score"].(float64), sharp["score"].(float64),
		"a defocused frame must score lower")
	assert.Positive(t, soft["distance_um"],
		"the meter must tell the user HOW FAR the focuser is out")

	// Racking back in must be recognised as an improvement — the only honest way to give direction.
	_, _ = post(t, ts, "/live/simulate", map[string]any{"focus_offset_um": 50.0})
	back := focusAfterSettling(t, ts)
	assert.Less(t, back["hfd_px"].(float64), softHFD)
	// Once the meter has settled at the new focus it reports better/steady/at-focus — never that
	// racking IN from 300 µm made things worse.
	assert.Contains(t, []any{"better", "steady", "at_focus"}, back["advice"])
}

func TestFocus_ResetForgetsTheSession(t *testing.T) {
	ts := focusServer(t)
	_, _ = post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/camera/control", map[string]any{"name": "exposure", "value": 500_000})
	_, _ = post(t, ts, "/live/simulate", map[string]any{
		"hot_pixels": 0,
		"star_grid":  map[string]any{"count": 4, "mag": 7.0, "spread_deg": 0.1},
	})
	_, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	t.Cleanup(func() { _, _ = post(t, ts, "/live/stop", nil) })
	liveFocus(t, ts)

	resp, body := post(t, ts, "/live/focus/reset", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["ok"])
}

// Every sensor has hot pixels, and to a peak finder they look like perfectly sharp stars. If they
// were measured, the focus meter would read "perfect" no matter where the focuser was — the exact
// failure that makes a focus aid worse than useless.
func TestFocus_IgnoresHotPixels(t *testing.T) {
	ts := focusServer(t)
	_, _ = post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/camera/control", map[string]any{"name": "exposure", "value": 500_000})
	// A badly defocused telescope on a sensor covered in defects. The stars are bright ones, as they
	// are when anybody actually focuses: at 400 µm out their light is spread over ~14 px, so a faint
	// star would sink into the noise for reasons that have nothing to do with hot pixels.
	_, _ = post(t, ts, "/live/simulate", map[string]any{
		"focus_offset_um": 400.0,
		"hot_pixels":      400,
		"star_grid":       map[string]any{"count": 4, "mag": 3.5, "spread_deg": 0.1},
	})
	_, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	t.Cleanup(func() { _, _ = post(t, ts, "/live/stop", nil) })

	f := liveFocus(t, ts)
	assert.Greater(t, f["hfd_px"].(float64), 5.0,
		"a 400 µm defocus must read as badly out of focus even with 400 hot pixels in frame")
	assert.Less(t, f["score"].(float64), 60.0)
}
