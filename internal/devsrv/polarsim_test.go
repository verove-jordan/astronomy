package devsrv

import (
	"context"
	"fmt"
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

	resp, _ := post(t, ts, "/camera/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, _ = post(t, ts, "/mount/connect", map[string]any{"driver": "sim"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/live/start", map[string]any{"interval_ms": 10})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Cleanup(func() { post(t, ts, "/live/stop", nil) })
	waitForLiveFrame(t, ts)

	// The measurement is taken twice and the answers subtracted, because the simulated mount is not
	// perfectly aligned to begin with — and cannot be.
	//
	// It holds J2000 coordinates, as everything else in the simulator does, so the sweep it traces is a
	// circle about the J2000 pole. Today's pole is about nine arcminutes away from that, so the ideal
	// simulated mount reads as nine arcminutes out before anything is injected. (Which is not even
	// unrealistic: it is what a mount aligned to a printed J2000 chart and never touched since would
	// do.) Differencing two measurements isolates exactly what was dialled in, which is what this test
	// is about.
	baseline := measurePolarError(t, ts, 0, 0)
	knocked := measurePolarError(t, ts, wantAltArcmin, wantAzArcmin)

	assert.InDelta(t, wantAltArcmin, (knocked.AltErrorDeg-baseline.AltErrorDeg)*60, 1,
		"altitude: dialled in %d′, measured %.1f′ against a %.1f′ baseline",
		wantAltArcmin, knocked.AltErrorDeg*60, baseline.AltErrorDeg*60)
	assert.InDelta(t, wantAzArcmin, (knocked.AzKnobDeg-baseline.AzKnobDeg)*60, 1,
		"azimuth: dialled in %d′, measured %.1f′ against a %.1f′ baseline",
		wantAzArcmin, knocked.AzKnobDeg*60, baseline.AzKnobDeg*60)

	// And the instructions have to point the right way for the error that was actually injected.
	assert.Equal(t, polaralign.MoveLower, knocked.AltMove, "the axis was raised, so it must be lowered")
	assert.Equal(t, polaralign.MoveEast, knocked.AzMove, "the axis was moved west, so it must go east")
}

// measurePolarError dials an error into the simulated observatory and runs the whole measurement
// against it: point, sweep the right-ascension axis, solve each frame, fit.
func measurePolarError(t *testing.T, ts *httptest.Server, altArcmin, azArcmin float64) polaralign.Correction {
	t.Helper()
	site := polaralign.Site{LatDeg: 48.85, LonDeg: 2.35}

	resp, _ := post(t, ts, "/live/simulate", map[string]any{
		"polar_error_alt_arcmin": altArcmin,
		"polar_error_az_arcmin":  azArcmin,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = post(t, ts, "/mount/goto", map[string]any{
		"ra_deg": startPointing(site), "dec_deg": sweepDecDeg,
	})
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	waitUntilStopped(t, ts)

	solver := platesolve.NewSimSolver()
	dir := t.TempDir()
	var samples []polaralign.Sample
	for i := 0; i < 4; i++ {
		if i > 0 {
			turnRAAxis(t, ts, 20)
		}
		path := filepath.Join(dir, fmt.Sprintf("polar_%d.fit", i))
		mid := saveFreshLiveFrame(t, ts, path)

		res, err := solver.Solve(context.Background(), path, platesolve.Hint{})
		require.NoError(t, err)
		samples = append(samples, polaralign.Sample{RADeg: res.RADeg, DecDeg: res.DecDeg, At: mid})
	}

	axis, err := polaralign.FitAxis(samples, site, polaralign.FitOptions{})
	require.NoError(t, err)
	require.Empty(t, axis.Warnings, "the sweep should be a clean measurement")
	require.Less(t, axis.ResidualArcsec, 5.0, "the frames should lie on one circle")
	return polaralign.Correct(axis, site)
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

// sweepDecDeg is where the sweep is taken. Well away from the pole, so the circle the frames trace has
// a usable radius, and high enough over Paris at any hour to stay clear of the horizon.
const sweepDecDeg = 20

// startPointing puts the telescope thirty degrees east of the meridian, so that sweeping sixty degrees
// west stays high in the sky whatever time the suite runs at.
func startPointing(site polaralign.Site) float64 {
	return math.Mod(astro.LST(time.Now().UTC(), site.LonDeg)+30+360, 360)
}

// turnRAAxis moves the RIGHT-ASCENSION axis by roughly deltaDeg, and nothing else.
//
// It jogs rather than slewing to a computed position, because that is what the procedure actually asks
// of the user — one axis, turned by hand — and because a GoTo does not do it. The simulated mount lands
// a deliberate arcminute off every target it is SENT to, which is realistic and correct; but four
// independent arcminute offsets do not lie on one circle, and the fit reads that scatter as a polar
// error many times larger than the one being tested. Jogging keeps declination untouched and adds no
// pointing error, so the frames stay on the circle exactly as they do on a real mount.
func turnRAAxis(t *testing.T, ts *httptest.Server, deltaDeg float64) {
	t.Helper()
	_, body := get(t, ts, "/mount")
	start := mountField(body, "ra_deg")

	for i := 0; i < 1000; i++ {
		_, body = get(t, ts, "/mount")
		if math.Abs(math.Mod(mountField(body, "ra_deg")-start+540, 360)-180) >= deltaDeg {
			return
		}
		post(t, ts, "/mount/jog", map[string]any{"direction": "west", "rate": 9})
	}
	t.Fatalf("the simulated axis never turned %g°", deltaDeg)
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
