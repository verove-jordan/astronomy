package devsrv

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/polaralign"
)

// The simulator harness, end to end at the level it exists to serve: dial a polar error into the
// simulated observatory, take frames through the real HTTP surface, solve them with the simulated
// solver, and require the measurement to recover the numbers that were dialled in.
//
// This is what makes the camera-alignment feature developable indoors. Without it the first time
// anything runs is on a real mount under real stars, which is the worst possible place to find out
// that a sign is backwards.

func TestSimHarness_RecoversTheInjectedPolarError(t *testing.T) {
	const (
		wantAltArcmin = 24
		wantAzArcmin  = -15
	)
	ts := testServer(t)
	ctx := context.Background()

	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/live/simulate", map[string]any{
		"polar_error_alt_arcmin": wantAltArcmin,
		"polar_error_az_arcmin":  wantAzArcmin,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() { post(t, ts, "/live/stop", nil) })
	waitForLiveFrame(t, ts)

	// Sweep the right-ascension axis the way the user is asked to: same declination, stepped hour
	// angle. On a misaligned mount that traces a circle about the tilted axis, which is the whole
	// signal the fit reads.
	solver := platesolve.NewSimSolver()
	dir := t.TempDir()
	site := polaralign.Site{LatDeg: 48.85, LonDeg: 2.35}

	var samples []polaralign.Sample
	for i := 0; i < 4; i++ {
		ra, dec := sweptPointing(t, ts, site, float64(i)*20)
		resp, _ = post(t, ts, "/mount/goto", map[string]any{"ra_deg": ra, "dec_deg": dec})
		require.Equal(t, http.StatusAccepted, resp.StatusCode, "step %d", i+1)
		waitUntilStopped(t, ts)

		path := filepath.Join(dir, "polar_"+string(rune('a'+i))+".fit")
		saved := saveFreshLiveFrame(t, ts, path)

		res, err := solver.Solve(ctx, path, platesolve.Hint{})
		require.NoError(t, err)
		samples = append(samples, polaralign.Sample{
			RADeg: res.RADeg, DecDeg: res.DecDeg, At: saved,
		})
	}

	axis, err := polaralign.FitAxis(samples, site, polaralign.FitOptions{})
	require.NoError(t, err)
	got := polaralign.Correct(axis, site)

	assert.InDelta(t, wantAltArcmin, got.AltErrorDeg*60, 2,
		"altitude error: dialled in %d′, measured %.1f′", wantAltArcmin, got.AltErrorDeg*60)
	assert.InDelta(t, wantAzArcmin, got.AzKnobDeg*60, 2,
		"azimuth error: dialled in %d′, measured %.1f′", wantAzArcmin, got.AzKnobDeg*60)
	assert.Equal(t, polaralign.MoveLower, got.AltMove)
	assert.Equal(t, polaralign.MoveEast, got.AzMove)
}

// A simulated observatory with no error configured has to behave as it always did. Zero being exactly
// a no-op is pinned in polaralign; what this checks is that the wiring honours it — that asking for no
// misalignment does not quietly leave some behind.
//
// The readings are compared loosely rather than exactly because the simulated worm keeps turning
// between them: the periodic error is real motion, and demanding bit-equality would be testing the
// clock rather than the alignment.
func TestSimHarness_ZeroErrorLeavesThePointingAlone(t *testing.T) {
	ts := testServer(t)
	post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})

	_, before := get(t, ts, "/mount")
	post(t, ts, "/live/simulate", map[string]any{
		"polar_error_alt_arcmin": 0, "polar_error_az_arcmin": 0,
	})
	_, after := get(t, ts, "/mount")

	const milliarcsec = 1.0 / 3600 / 1000
	assert.InDelta(t, mountField(before, "ra_deg"), mountField(after, "ra_deg"), milliarcsec)
	assert.InDelta(t, mountField(before, "dec_deg"), mountField(after, "dec_deg"), milliarcsec)

	// And a real error does move it, so the test above is not passing for want of any wiring at all.
	post(t, ts, "/live/simulate", map[string]any{
		"polar_error_alt_arcmin": 30, "polar_error_az_arcmin": 0,
	})
	_, knocked := get(t, ts, "/mount")
	assert.Greater(t,
		math.Abs(mountField(knocked, "dec_deg")-mountField(after, "dec_deg")), 0.001,
		"half a degree of polar error has to move the reported pointing")
}

// The simulated solver must refuse a frame it did not draw, rather than inventing a plate solution for
// somebody's real photograph.
func TestSimSolver_RefusesARealFrame(t *testing.T) {
	ts := testServer(t)
	post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	t.Cleanup(func() { post(t, ts, "/live/stop", nil) })
	waitForLiveFrame(t, ts)

	path := filepath.Join(t.TempDir(), "frame.fit")
	saveFreshLiveFrame(t, ts, path)

	// The simulated frame solves.
	_, err := platesolve.NewSimSolver().Solve(context.Background(), path, platesolve.Hint{})
	require.NoError(t, err)

	// A file without the truth cards does not.
	bare := filepath.Join(t.TempDir(), "bare.fit")
	writeBareFrame(t, bare)
	_, err = platesolve.NewSimSolver().Solve(context.Background(), bare, platesolve.Hint{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a simulated frame")
}

// --- helpers ---

// sweptPointing is where to send the mount for step `haOffsetDeg` of the sweep: same declination,
// stepped hour angle, which is the motion the user is asked to make by hand.
func sweptPointing(t *testing.T, ts *httptest.Server, site polaralign.Site, haOffsetDeg float64) (ra, dec float64) {
	t.Helper()
	const decDeg = 60 // high declination keeps each step a short slew, so the test stays quick
	lst := astro.LST(time.Now().UTC(), site.LonDeg)
	return math.Mod(lst-(-30+haOffsetDeg)+360, 360), decDeg
}

func waitUntilStopped(t *testing.T, ts *httptest.Server) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, body := get(t, ts, "/mount")
		mount, _ := body["mount"].(map[string]any)
		slewing, _ := mount["slewing"].(bool)
		return !slewing
	}, 60*time.Second, 50*time.Millisecond, "the simulated mount never stopped slewing")
}

// saveFreshLiveFrame writes a live frame that was STARTED after this call, and returns its
// mid-exposure instant. A frame begun while the mount was still moving records a pointing part way
// through the slew, which is not on the circle the fit is looking for.
func saveFreshLiveFrame(t *testing.T, ts *httptest.Server, path string) time.Time {
	t.Helper()
	_, before := post(t, ts, "/live/save", map[string]any{"path": path + ".probe"})
	startSeq, _ := before["seq"].(float64)

	var saved map[string]any
	require.Eventually(t, func() bool {
		_, body := post(t, ts, "/live/save", map[string]any{
			"path": path, "type": "light", "object": "polar-align",
		})
		seq, _ := body["seq"].(float64)
		if seq < startSeq+2 { // +2: the frame in flight when we asked began before we asked
			return false
		}
		saved = body
		return true
	}, 30*time.Second, 20*time.Millisecond, "no fresh live frame arrived")

	startedAt, err := time.Parse(time.RFC3339Nano, saved["started_at"].(string))
	require.NoError(t, err)
	expUs, _ := saved["exposure_us"].(float64)
	return startedAt.Add(time.Duration(expUs/2) * time.Microsecond)
}

func mountField(body map[string]any, key string) float64 {
	mount, _ := body["mount"].(map[string]any)
	v, _ := mount[key].(float64)
	return v
}

func writeBareFrame(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, fits.Write16(path, 8, 8, make([]uint16, 64), nil))
}
