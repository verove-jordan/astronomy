package sim

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// testClock drives simulated time by hand, so worm phase is exact rather than raced against wall
// clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 7, 29, 22, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// pecWorld builds a world with a clean, fully deterministic worm: no jitter, no hot pixels.
func pecWorld(t *testing.T, cfg Config) (*World, *testClock) {
	t.Helper()
	cfg.HotPixels = -1
	cfg.PEJitterArcsec = -1
	if cfg.PEPeriodSec == 0 {
		cfg.PEPeriodSec = 478
	}
	w := NewWorld(cfg)
	clk := newTestClock()
	w.SetClock(clk.Now)
	return w, clk
}

func wormElapsed(w *World, at time.Time) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wormElapsedLocked(at)
}

func pointing(w *World, at time.Time) (float64, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pointingAt(at)
}

// The worm turns only while the drive does. This is the whole reason the geometry calibration can
// stop tracking for ten seconds without corrupting the phase it is measuring against — and it used
// to be wrong: SetTracking reset the phase origin, teleporting the worm.
func TestWorld_WormPausesWhileTrackingIsOff(t *testing.T) {
	w, clk := pecWorld(t, Config{})
	m := NewMount(w)
	require.NoError(t, m.Connect(context.Background()))
	ctx := context.Background()

	clk.advance(100 * time.Second)
	assert.InDelta(t, 100, wormElapsed(w, clk.Now()), 0.001)

	require.NoError(t, m.SetTracking(ctx, false, ""))
	clk.advance(200 * time.Second)
	assert.InDelta(t, 100, wormElapsed(w, clk.Now()), 0.001,
		"the worm does not turn while the drive is off")

	require.NoError(t, m.SetTracking(ctx, true, "sidereal"))
	clk.advance(50 * time.Second)
	assert.InDelta(t, 150, wormElapsed(w, clk.Now()), 0.001,
		"and it resumes from where it stopped, not from zero")
}

// Periodic error is an error of the AXIS, so the RA coordinate offset it produces is the same at any
// declination — and what lands on the sensor is that times cos(dec). If this were modelled the other
// way round, code that forgot to divide a pixel measurement by cos(dec) would still pass.
func TestWorld_PeriodicErrorIsAnAxisErrorNotASkyError(t *testing.T) {
	const amplitude = 12.0
	offsets := make(map[string]float64)

	for name, dec := range map[string]float64{"equator": 0, "high north": 60} {
		w, clk := pecWorld(t, Config{PEAmplitude: amplitude, StartRADeg: 100, StartDecDeg: dec})
		// A quarter of the way round the worm puts the sine at its peak.
		clk.advance(time.Duration(478/4) * time.Second)
		ra, _ := pointing(w, clk.Now())
		offsets[name] = (ra - 100) * 3600 // RA-coordinate arcsec
	}

	assert.InDelta(t, offsets["equator"], offsets["high north"], 0.2,
		"the axis error does not depend on declination")
	assert.InDelta(t, amplitude/2, offsets["equator"], 0.2, "half the peak-to-peak, at the peak")
}

// A curve the mount plays back must actually move the simulated telescope, or the round-trip test
// proves nothing.
func TestMount_PECPlayback_MovesThePointing(t *testing.T) {
	w, clk := pecWorld(t, Config{PEAmplitude: -1, StartRADeg: 100, StartDecDeg: 0})
	m := NewMount(w)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))

	caps, err := m.PECCaps(ctx)
	require.NoError(t, err)
	require.Equal(t, 88, caps.Bins)

	// A constant positive rate across every bin: after one bin's worth of time the pointing must
	// have moved by exactly rate × time.
	curve := make([]int8, caps.Bins)
	for i := range curve {
		curve[i] = 10
	}
	require.NoError(t, m.PECWriteCurve(ctx, curve))

	before, _ := pointing(w, clk.Now())
	require.NoError(t, m.PECPlayback(ctx, true))
	clk.advance(time.Duration(caps.BinSec * float64(time.Second)))
	after, _ := pointing(w, clk.Now())

	wantArcsec := 10 * caps.LSBArcsecPerSec * caps.BinSec
	assert.InDelta(t, wantArcsec, (after-before)*3600, 0.01)
}

// Enabling playback mid-run must not teleport: the mount starts correcting from now, it does not
// retroactively apply everything the table would have accumulated since the index.
func TestMount_PECPlayback_IsContinuousWhenSwitched(t *testing.T) {
	w, clk := pecWorld(t, Config{PEAmplitude: -1, StartRADeg: 100, StartDecDeg: 0})
	m := NewMount(w)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))

	curve := make([]int8, 88)
	for i := range curve {
		curve[i] = 40
	}
	require.NoError(t, m.PECWriteCurve(ctx, curve))
	clk.advance(300 * time.Second) // well into the worm cycle

	before, _ := pointing(w, clk.Now())
	require.NoError(t, m.PECPlayback(ctx, true))
	after, _ := pointing(w, clk.Now())
	assert.InDelta(t, before, after, 1e-9, "switching playback on is not a step")

	clk.advance(100 * time.Second)
	moved, _ := pointing(w, clk.Now())
	require.NoError(t, m.PECPlayback(ctx, false))
	stopped, _ := pointing(w, clk.Now())
	assert.InDelta(t, moved, stopped, 1e-9,
		"switching it off does not undo motion that already happened")
}

// The table holds rates, so its values summing to something other than zero makes the mount drift a
// little further every revolution. That is a real failure mode of a badly computed curve, and the
// simulator has to reproduce it rather than quietly reset the integral each cycle.
func TestMount_PECPlayback_NonZeroMeanCurveDrifts(t *testing.T) {
	tests := []struct {
		name      string
		fill      func(i int) int8
		wantDrift bool
	}{
		{
			name:      "zero-mean curve",
			fill:      func(i int) int8 { return int8(20 * math.Sin(2*math.Pi*float64(i)/88)) },
			wantDrift: false,
		},
		{
			name:      "curve with a net rate",
			fill:      func(int) int8 { return 5 },
			wantDrift: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, clk := pecWorld(t, Config{PEAmplitude: -1, StartRADeg: 100, StartDecDeg: 0})
			m := NewMount(w)
			ctx := context.Background()
			require.NoError(t, m.Connect(ctx))

			curve := make([]int8, 88)
			for i := range curve {
				curve[i] = tt.fill(i)
			}
			require.NoError(t, m.PECWriteCurve(ctx, curve))

			start, _ := pointing(w, clk.Now())
			require.NoError(t, m.PECPlayback(ctx, true))
			clk.advance(10 * 478 * time.Second) // ten whole worm revolutions
			end, _ := pointing(w, clk.Now())

			driftArcsec := math.Abs(end-start) * 3600
			if tt.wantDrift {
				assert.Greater(t, driftArcsec, 100.0, "a net rate accumulates every revolution")
			} else {
				assert.Less(t, driftArcsec, 0.01, "a zero-mean curve returns where it started")
			}
		})
	}
}

// The bin index is the phase reference a training run folds on.
func TestMount_PECBin_TracksTheWormAndWraps(t *testing.T) {
	w, clk := pecWorld(t, Config{PEAmplitude: -1})
	m := NewMount(w)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))

	caps, err := m.PECCaps(ctx)
	require.NoError(t, err)

	bin, err := m.PECBin(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, bin)

	clk.advance(time.Duration(3.5 * caps.BinSec * float64(time.Second)))
	bin, err = m.PECBin(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, bin)

	// One whole revolution later it is back where it started.
	clk.advance(time.Duration(caps.WormPeriodSec * float64(time.Second)))
	wrapped, err := m.PECBin(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, wrapped)
}

func TestMount_PECSeekIndex_SetsTheIndex(t *testing.T) {
	w, _ := pecWorld(t, Config{})
	m := NewMount(w)
	ctx := context.Background()
	require.NoError(t, m.Connect(ctx))

	st, err := m.PECStatus(ctx)
	require.NoError(t, err)
	require.False(t, st.Indexed, "a freshly powered mount has not found its index")

	require.NoError(t, m.PECSeekIndex(ctx))

	st, err = m.PECStatus(ctx)
	require.NoError(t, err)
	assert.True(t, st.Indexed)
	assert.False(t, st.Seeking)
}

func TestMount_PECWriteCurve_RejectsAWrongLengthCurve(t *testing.T) {
	w, _ := pecWorld(t, Config{})
	m := NewMount(w)
	require.NoError(t, m.Connect(context.Background()))

	err := m.PECWriteCurve(context.Background(), make([]int8, 42))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "42 bins")
}

func TestMount_PEC_RequiresAConnection(t *testing.T) {
	w, _ := pecWorld(t, Config{})
	m := NewMount(w)
	ctx := context.Background()

	_, err := m.PECCaps(ctx)
	assert.ErrorIs(t, err, device.ErrNotConnected)
	_, err = m.PECBin(ctx)
	assert.ErrorIs(t, err, device.ErrNotConnected)
	assert.ErrorIs(t, m.PECPlayback(ctx, true), device.ErrNotConnected)
}

// Jitter is the part of the error that does not repeat, and so the part PEC can never remove.
// Without it every simulated mount is perfectly coherent and the repeatability gate is untested.
func TestWorld_JitterDoesNotRepeatOnTheWormPeriod(t *testing.T) {
	w := NewWorld(Config{HotPixels: -1, PEAmplitude: -1, PEJitterArcsec: 2, PEPeriodSec: 478})
	clk := newTestClock()
	w.SetClock(clk.Now)

	w.mu.Lock()
	defer w.mu.Unlock()
	first := w.periodicErrorArcsecLocked(clk.Now().Add(120 * time.Second))
	next := w.periodicErrorArcsecLocked(clk.Now().Add((120 + 478) * time.Second))

	assert.Greater(t, math.Abs(first-next), 0.05,
		"a component that repeated every worm cycle would be correctable, and jitter is not")
}
