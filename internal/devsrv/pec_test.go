package devsrv

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPEC_ReportsNotConnectedBeforeAMountIsOpened(t *testing.T) {
	ts := testServer(t)

	resp, body := get(t, ts, "/pec")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["connected"])
	assert.Equal(t, false, body["supported"])
}

func TestPEC_ReportsTheWormGeometryFromTheMount(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	resp, body := get(t, ts, "/pec")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, true, body["supported"])

	caps, _ := body["caps"].(map[string]any)
	require.NotNil(t, caps)
	assert.Equal(t, float64(88), caps["bins"])
	assert.InDelta(t, 478, caps["worm_period_sec"], 1)
	assert.InDelta(t, 5.43, caps["bin_sec"], 0.05)
	assert.InDelta(t, 0.01469, caps["lsb_arcsec_per_sec"], 0.0001)

	status, _ := body["status"].(map[string]any)
	require.NotNil(t, status)
	assert.Equal(t, false, status["indexed"], "a freshly powered mount has not found its index")
	assert.Equal(t, false, status["playing"])
}

func TestPEC_ReadsTheStoredCurve(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	resp, body := get(t, ts, "/pec/curve")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	bins, _ := body["bins"].([]any)
	assert.Len(t, bins, 88, "the whole table comes back, so it can be backed up before anything writes")
}

// The nightly win: a Celestron keeps its curve across power-off but comes up with playback off and
// the index unfound, so a mount with a perfectly good table tracks as if it had none.
func TestPEC_EnableSeeksTheIndexThenStartsPlayback(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	resp, body := post(t, ts, "/pec/enable", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["sought_index"],
		"the caller has to know framing moved — a seek turns RA by up to two degrees")

	status, _ := body["status"].(map[string]any)
	require.NotNil(t, status)
	assert.Equal(t, true, status["indexed"])
	assert.Equal(t, true, status["playing"])
}

// Enabling twice must not seek again: the index is already found, and a second seek would throw away
// the framing for nothing.
func TestPEC_EnableIsIdempotentOnceIndexed(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	_, first := post(t, ts, "/pec/enable", nil)
	require.Equal(t, true, first["sought_index"])

	_, second := post(t, ts, "/pec/enable", nil)
	assert.Equal(t, false, second["sought_index"], "the index was already found")
	status, _ := second["status"].(map[string]any)
	assert.Equal(t, true, status["playing"])
}

// Playback must be switchable off, because measuring the worm with it on yields the RESIDUAL error
// and a curve computed from that silently under-corrects.
func TestPEC_PlaybackTogglesOff(t *testing.T) {
	ts := testServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/pec/enable", nil)

	resp, body := post(t, ts, "/pec/playback", map[string]any{"on": false})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	status, _ := body["status"].(map[string]any)
	assert.Equal(t, false, status["playing"])
}
