package devsrv

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/device/sim"
)

// The end-to-end round trip: give a simulated mount a known worm error, let the training run measure
// it, write a curve and switch playback on, then measure again and insist it got better.
//
// This is the test the previous, plate-solve-based measurement could never have had — simulated
// frames do not plate-solve. Centroids do not care, which is the whole reason the measurement was
// built this way.
//
// The worm period is compressed to a few seconds so a four-revolution run costs seconds rather than
// half an hour. Nothing in the pipeline may hardcode 478 or 88 for that to work, which this test
// therefore also enforces.

// pecTestServer wires a device server onto a simulated observatory with the given worm.
func pecTestServer(t *testing.T, world sim.Config) *Server {
	t.Helper()
	cfg := &config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 220, SensorHpx: 220,
		ApertureMM: 100, LatDeg: 48.85, LonDeg: 2.35, DeviceAddr: "127.0.0.1:0",
	}
	world.SensorW, world.SensorH = cfg.SensorWpx, cfg.SensorHpx
	world.FocalMM, world.PixelUm = cfg.FocalLenMM, cfg.PixelSizeUm
	world.ApertureMM = cfg.ApertureMM
	world.HotPixels = -1
	// One known bright star and nothing else: a real field a few arcminutes across can be empty, and
	// thousands of faint stars would give the tracker neighbours to confuse it with.
	world.FaintStarsPerDeg2 = -1
	world.StartRADeg, world.StartDecDeg = 10.6847, 0
	world.SyntheticStars = []sim.SyntheticStar{{RADeg: 10.6847, DecDeg: 0, Mag: 2.5}}

	srv := New(cfg)
	srv.world = sim.NewWorld(world)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	cam, err := srv.openCamera(DriverSim)
	require.NoError(t, err)
	require.NoError(t, cam.Connect(ctx))
	mount, err := srv.openMount(DriverSim)
	require.NoError(t, err)
	require.NoError(t, mount.Connect(ctx))

	srv.mu.Lock()
	srv.camera, srv.mount = cam, mount
	srv.mu.Unlock()
	return srv
}

// waitForPEC blocks until the run finishes.
func waitForPEC(t *testing.T, srv *Server, timeout time.Duration) PECTrainState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !srv.pecTrain.isRunning() {
			return srv.pecTrain.snapshot()
		}
		time.Sleep(20 * time.Millisecond)
	}
	srv.pecTrain.stop()
	t.Fatalf("the training run did not finish within %s (state %+v)", timeout, srv.pecTrain.snapshot())
	return PECTrainState{}
}

// The whole point, in one test.
func TestPECTrain_MeasuresWritesAndImprovesTracking(t *testing.T) {
	srv := pecTestServer(t, sim.Config{
		PEAmplitude:    14, // arcsec peak-to-peak, as an axis error
		PEPeriodSec:    6,  // a compressed worm, so the run costs seconds
		PEJitterArcsec: -1, // a perfectly repeatable mount, so the gate cannot be the thing under test
		SeeingArcsec:   1.5,
		// A correction is a RATE, so compressing the worm eighty-fold multiplies every rate by
		// eighty. A table scaled for an eight-minute worm cannot express that, so this simulated
		// mount gets a correspondingly coarser one.
		PECRateScale: 16,
	})

	require.NoError(t, srv.pecTrain.start(PECTrainRequest{
		ExposureSec: 0.02, Cycles: 5, DriftSec: 1.2, Write: true,
	}))
	state := waitForPEC(t, srv, 90*time.Second)

	require.Equal(t, PECDone, state.Phase, "run failed: %s", state.Error)
	require.NotNil(t, state.Report)
	require.NotNil(t, state.Calibration)

	// The calibration must have recovered roughly the true image scale. It is measured against the
	// sky, so it carries cos(dec) — and this test sits on the equator, where that factor is one.
	assert.InDelta(t, 1.06, state.Calibration.AxisArcsecPerPx, 0.15)

	assert.InDelta(t, 14, state.Report.PeakToPeakArcsec, 4,
		"the injected periodic error must come back out")
	assert.Greater(t, state.Report.Coherent, 0.5, "a jitter-free worm repeats")

	require.NotNil(t, state.Improvement, "the run must check its own work")
	assert.False(t, state.Reverted, "a correctly signed curve should not have been rolled back")
	assert.Greater(t, state.Improvement.AmplitudeRatio(), 2.0,
		"writing the curve must at least halve the periodic error")
	assert.Greater(t, state.Improvement.AfterMaxUnguided, state.Improvement.BeforeMaxUnguided,
		"and that has to show up as a longer usable exposure")

	assert.Len(t, state.Backup, 88, "the curve that was there before is kept")
	assert.Len(t, state.Written, 88)
}

// The safety net, against the failure it exists for.
//
// The table's sign convention is undocumented, and a mount whose firmware differs from what we assumed
// does not report an error — it simply tracks about twice as badly, quietly, all night. This drives a
// simulated mount that applies the curve backwards and insists the run notices, puts the original
// curve back, and says why.
func TestPECTrain_RevertsWhenTheMountAppliesTheCurveBackwards(t *testing.T) {
	srv := pecTestServer(t, sim.Config{
		PEAmplitude:    14,
		PEPeriodSec:    6,
		PEJitterArcsec: -1,
		SeeingArcsec:   1.5,
		PECRateScale:   16,
		PECInvertSign:  true, // a firmware that reads the table the other way round
	})
	ctx := context.Background()

	pecMount := srv.pecMount()
	before, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)

	require.NoError(t, srv.pecTrain.start(PECTrainRequest{
		ExposureSec: 0.02, Cycles: 5, DriftSec: 1.2, Write: true,
	}))
	state := waitForPEC(t, srv, 120*time.Second)

	require.Equal(t, PECDone, state.Phase, "reverting is an orderly outcome, not a crash: %s", state.Error)
	require.NotNil(t, state.Improvement)
	assert.True(t, state.Reverted, "the mount got worse, so the old curve must be back")
	assert.Less(t, state.Improvement.AmplitudeRatio(), 1.0)
	assert.Contains(t, state.Message, "restored")

	after, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "restored byte for byte")

	st, err := pecMount.PECStatus(ctx)
	require.NoError(t, err)
	assert.False(t, st.Playing, "and a curve that made things worse must not be left playing")
}

// Measuring without writing must leave the mount exactly as it was found.
func TestPECTrain_MeasureOnlyNeverTouchesTheTable(t *testing.T) {
	srv := pecTestServer(t, sim.Config{PEAmplitude: 12, PEPeriodSec: 5, PEJitterArcsec: -1})
	ctx := context.Background()

	pecMount := srv.pecMount()
	require.NotNil(t, pecMount)
	before, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)

	require.NoError(t, srv.pecTrain.start(PECTrainRequest{
		ExposureSec: 0.02, Cycles: 3, DriftSec: 1.0, Write: false,
	}))
	state := waitForPEC(t, srv, 60*time.Second)

	require.Equal(t, PECDone, state.Phase, "run failed: %s", state.Error)
	require.NotNil(t, state.Report)
	assert.Empty(t, state.Written)

	after, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a measure-only run must not write anything")

	st, err := pecMount.PECStatus(ctx)
	require.NoError(t, err)
	assert.False(t, st.Playing, "and must not leave playback running")
}

// A mount whose error does not repeat cannot be helped by a table that replays the same thing every
// revolution, and saying so is more useful than replaying that night's seeing for ever.
func TestPECTrain_RefusesToWriteWhenTheErrorDoesNotRepeat(t *testing.T) {
	srv := pecTestServer(t, sim.Config{
		PEAmplitude: 2, // barely any real worm error…
		PEPeriodSec: 5,
		// …under a large component that is never in the same place twice.
		PEJitterArcsec: 14,
		SeeingArcsec:   1.5,
	})
	ctx := context.Background()

	pecMount := srv.pecMount()
	before, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)

	require.NoError(t, srv.pecTrain.start(PECTrainRequest{
		ExposureSec: 0.02, Cycles: 4, DriftSec: 1.0, Write: true,
	}))
	state := waitForPEC(t, srv, 60*time.Second)

	require.Equal(t, PECFailed, state.Phase)
	assert.Contains(t, state.Error, "repeats")
	require.NotNil(t, state.Report)
	assert.Less(t, state.Report.Coherent, 0.5)

	after, err := pecMount.PECReadCurve(ctx)
	require.NoError(t, err)
	assert.Equal(t, before, after, "nothing may be written when the gate refuses")
}

// The tracking is switched off to calibrate, and a run that ends any other way must still put it
// back — a telescope left with the drive off slides away from the target.
func TestPECTrain_AlwaysLeavesTheDriveRunning(t *testing.T) {
	srv := pecTestServer(t, sim.Config{PEAmplitude: 10, PEPeriodSec: 4, PEJitterArcsec: -1})
	ctx := context.Background()

	require.NoError(t, srv.pecTrain.start(PECTrainRequest{
		ExposureSec: 0.02, Cycles: 3, DriftSec: 1.0, Write: false,
	}))
	// Interrupt it partway, which is the case most likely to leave things half-done.
	time.Sleep(1500 * time.Millisecond)
	srv.pecTrain.stop()
	waitForPEC(t, srv, 30*time.Second)

	st, err := srv.currentMount().State(ctx)
	require.NoError(t, err)
	assert.True(t, st.Tracking, "the drive must be running whatever happened")
}

// The worm phase comes from the mount's own bin counter, and the midpoint of the two reads that
// straddle an exposure is what the sample is folded on. Wrapping past the index must not put the
// sample on the far side of the worm.
func TestMidBin_HandlesTheWrapPastTheIndex(t *testing.T) {
	tests := []struct {
		name                string
		before, after, bins int
		want                float64
	}{
		{"within a bin", 10, 10, 88, 10},
		{"across one bin", 10, 12, 88, 11},
		{"wrapping past the index", 87, 1, 88, 0},
		{"wrapping onto the index", 86, 0, 88, 87},
		{"a stalled or impossible jump", 10, 70, 88, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := midBin(tt.before, tt.after, tt.bins)
			assert.InDelta(t, tt.want, got, 0.001)
			assert.GreaterOrEqual(t, got, 0.0)
			assert.Less(t, got, float64(tt.bins))
		})
	}
}

// The calibration turns pixels into axis arcseconds along the RA direction, and ignores motion
// across it.
func TestPECCalibration_ProjectsOntoTheAxis(t *testing.T) {
	cal := PECCalibration{AxisArcsecPerPx: 2, UnitX: 1, UnitY: 0}
	assert.InDelta(t, 6, cal.AxisArcsec(3, 0), 1e-9)
	assert.InDelta(t, 0, cal.AxisArcsec(0, 3), 1e-9, "motion across the axis is not worm error")
	assert.InDelta(t, -6, cal.AxisArcsec(-3, 5), 1e-9)

	// A rotated camera changes which pixel direction the axis lies along, and nothing else.
	diag := PECCalibration{AxisArcsecPerPx: 1, UnitX: math.Sqrt2 / 2, UnitY: math.Sqrt2 / 2}
	assert.InDelta(t, math.Sqrt2, diag.AxisArcsec(1, 1), 1e-9)
}
