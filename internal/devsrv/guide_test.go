package devsrv

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guideServer is a device server with the simulated mount connected.
func guideServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := testServer(t)
	resp, _ := post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return ts
}

func TestGuideStatus_ReportsSupportAndRate(t *testing.T) {
	ts := guideServer(t)

	resp, body := get(t, ts, "/mount/guide")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["supported"])
	assert.InDelta(t, 0.5, body["rate_fraction"], 1e-9)
	// Half sidereal, so a caller need not know the sidereal constant to size a pulse.
	assert.InDelta(t, 7.52, body["rate_arcsec_per_sec"], 0.01)
}

func TestGuideStatus_SaysUnsupportedWithNoMount(t *testing.T) {
	ts := testServer(t)

	resp, body := get(t, ts, "/mount/guide")
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"absence of a mount is an ordinary answer, not an error")
	assert.Equal(t, false, body["supported"])
}

func TestGuidePulse_MovesTheMountAndReportsWhatItDid(t *testing.T) {
	ts := guideServer(t)

	_, before := get(t, ts, "/mount")
	beforeState, _ := before["mount"].(map[string]any)
	ra0, _ := beforeState["ra_deg"].(float64)

	resp, body := post(t, ts, "/mount/guide", map[string]any{
		"ra_arcsec": 6.0, "rate_arcsec_per_sec": 7.52,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	applied, _ := body["applied"].(map[string]any)
	require.Contains(t, applied, "ra")
	assert.NotContains(t, applied, "dec", "an axis with no correction must not be pulsed at all")

	raPulse, _ := applied["ra"].(map[string]any)
	assert.InDelta(t, 6.0, raPulse["arcsec"], 1e-9)
	assert.InDelta(t, 7.52, raPulse["arcsec_per_sec"], 1e-9)
	assert.InDelta(t, 798, raPulse["duration_ms"], 2, "6″ at 7.52″/s is about 0.8 s")

	_, after := get(t, ts, "/mount")
	afterState, _ := after["mount"].(map[string]any)
	ra1, _ := afterState["ra_deg"].(float64)
	assert.InDelta(t, 6.0, (ra1-ra0)*3600, 0.2, "the RA axis really turned by the commanded amount")
}

func TestGuidePulse_NegativeCorrectionReversesTheAxis(t *testing.T) {
	ts := guideServer(t)

	_, body := post(t, ts, "/mount/guide", map[string]any{
		"dec_arcsec": -8.0, "rate_arcsec_per_sec": 8.0,
	})
	applied, _ := body["applied"].(map[string]any)
	decPulse, _ := applied["dec"].(map[string]any)
	assert.InDelta(t, -8.0, decPulse["arcsec_per_sec"], 1e-9, "the rate carries the sign")
	assert.InDelta(t, 1000, decPulse["duration_ms"], 2)
}

func TestGuidePulse_DoesNothingWhenBothAxesAreZero(t *testing.T) {
	ts := guideServer(t)

	resp, body := post(t, ts, "/mount/guide", map[string]any{"rate_arcsec_per_sec": 7.52})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	applied, _ := body["applied"].(map[string]any)
	assert.Empty(t, applied, "a withheld correction must not reach the motor at all")
}

func TestGuidePulse_FallsBackToADefaultRate(t *testing.T) {
	ts := guideServer(t)

	// A caller that omits the rate must still get a sane pulse rather than a divide-by-zero or a
	// zero-length one that silently corrects nothing.
	_, body := post(t, ts, "/mount/guide", map[string]any{"ra_arcsec": 7.52})

	applied, _ := body["applied"].(map[string]any)
	raPulse, _ := applied["ra"].(map[string]any)
	assert.InDelta(t, 7.52, raPulse["arcsec_per_sec"], 0.01)
	assert.InDelta(t, 1000, raPulse["duration_ms"], 5)
}

func TestGuidePulse_RefusedWithNoMount(t *testing.T) {
	ts := testServer(t)

	resp, _ := post(t, ts, "/mount/guide", map[string]any{"ra_arcsec": 5.0})

	assert.GreaterOrEqual(t, resp.StatusCode, 400, "guiding nothing must fail loudly, not silently succeed")
}

func TestGuideSetRate_ClampsAndReadsBack(t *testing.T) {
	ts := guideServer(t)

	tests := []struct {
		name     string
		fraction float64
		want     float64
	}{
		{"ordinary", 0.75, 0.75},
		{"clamped high", 4, 1},
		{"clamped low", -2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := post(t, ts, "/mount/guide-rate", map[string]any{"fraction": tt.fraction})
			require.Equal(t, http.StatusOK, resp.StatusCode)
			// Read back rather than echoed: the driver quantises to the 1/256ths the wire carries, and the
			// caller should see what the mount will really do.
			assert.InDelta(t, tt.want, body["rate_fraction"], 1e-9)

			_, status := get(t, ts, "/mount/guide")
			assert.InDelta(t, tt.want, status["rate_fraction"], 1e-9, "and it must persist")
		})
	}
}
