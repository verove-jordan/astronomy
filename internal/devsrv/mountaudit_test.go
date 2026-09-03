package devsrv

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
)

// auditServer is testServer with somewhere safe to put a backup. A restore refuses to run without
// one, and a test that wrote into the repository's own output directory would be leaving litter in
// the user's night's work.
func auditServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	out := t.TempDir()
	cfg := &config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 256, SensorHpx: 256,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35,
		DeviceAddr: "127.0.0.1:0", OutputDir: out,
	}
	srv := New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return ts, out
}

func backupsIn(t *testing.T, dir string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, "mount-restore-*.json"))
	require.NoError(t, err)
	return found
}

// The panel has to be able to render "nothing is plugged in" rather than an error, the same way the
// mount status route already lets it.
func TestMountAudit_AnswersWithoutAMount(t *testing.T) {
	ts, _ := auditServer(t)

	resp, body := get(t, ts, "/mount/audit")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["connected"])
}

func TestMountAudit_ReadsTheMountAndNamesWhatItCannotRead(t *testing.T) {
	ts, _ := auditServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	resp, body := get(t, ts, "/mount/audit")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, true, body["connected"])

	audit, _ := body["audit"].(map[string]any)
	require.NotNil(t, audit)

	pec, _ := audit["pec"].(map[string]any)
	require.NotNil(t, pec)
	assert.Equal(t, true, pec["supported"])
	assert.Equal(t, float64(88), pec["bins"])
	assert.Equal(t, true, pec["all_zero"], "an untrained mount holds a table of zeros")

	// The simulator has no hand controller, so it stores no site — and that must arrive as an absence
	// the panel can render, not as a zero that reads like a real coordinate.
	site, _ := audit["site"].(map[string]any)
	require.NotNil(t, site)
	assert.Equal(t, false, site["read"])
	assert.NotEmpty(t, site["err"])

	notes, _ := audit["notes"].([]any)
	assert.NotEmpty(t, notes, "the report says in words what the numbers cannot")
}

func TestMountReset_RefusesWithoutAMount(t *testing.T) {
	ts, _ := auditServer(t)

	resp, _ := post(t, ts, "/mount/reset", map[string]any{"pec": true, "apply": true})
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
}

// A browser can reach this route, so not sending `apply` must cost a report rather than an hour of
// somebody's periodic-error recording.
func TestMountReset_IsADryRunUnlessAsked(t *testing.T) {
	ts, out := auditServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/pec/playback", map[string]any{"on": true})

	resp, body := post(t, ts, "/mount/reset", map[string]any{"pec": true, "pec_playback": true})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, true, body["dry_run"])

	actions, _ := body["actions"].([]any)
	require.Len(t, actions, 2)
	for _, a := range actions {
		assert.Equal(t, false, a.(map[string]any)["applied"])
	}

	// The backup is written even so: having the mount's own table on disk before anyone decides
	// anything costs nothing, and it is the only copy there is.
	files := backupsIn(t, out)
	require.Len(t, files, 1)
	saved, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Contains(t, string(saved), "\"curve\"")

	// And the mount is untouched.
	_, pecBody := get(t, ts, "/pec")
	status, _ := pecBody["status"].(map[string]any)
	assert.Equal(t, true, status["playing"], "a dry run stops nothing")
}

func TestMountReset_AppliesAndProvesItByReadingBack(t *testing.T) {
	ts, out := auditServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	_, _ = post(t, ts, "/pec/playback", map[string]any{"on": true})

	resp, body := post(t, ts, "/mount/reset", map[string]any{
		"pec": true, "pec_playback": true, "guide_rate": true, "apply": true,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, false, body["dry_run"])
	require.Len(t, backupsIn(t, out), 1)

	for _, a := range body["actions"].([]any) {
		act := a.(map[string]any)
		assert.Equal(t, true, act["applied"], "%v: %v", act["item"], act["err"])
	}

	after, _ := body["after"].(map[string]any)
	require.NotNil(t, after, "the after-reading is what proves it, not the write returning nil")
	pec, _ := after["pec"].(map[string]any)
	assert.Equal(t, true, pec["all_zero"])
	assert.Equal(t, false, pec["playback_commanded"])
}

// A site the caller names overrides the server's configured one, because the observing site the user
// actually edits lives in the browser where this process cannot read it.
func TestMountReset_TakesTheSiteFromTheRequestWhenGiven(t *testing.T) {
	ts, _ := auditServer(t)
	_, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	resp, body := post(t, ts, "/mount/reset", map[string]any{
		"site": true, "lat_deg": 43.6047, "lon_deg": 1.4442,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	actions, _ := body["actions"].([]any)
	require.Len(t, actions, 1)
	assert.Contains(t, actions[0].(map[string]any)["detail"], "43.6047")
}
