package capture

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/polaralign"
)

// The session is tested against a substituted solver, because simulated frames cannot be plate-solved
// (internal/platesolve/simsolve_test.go records why). What is under test here is the session — the
// phases, the frame gating, the error handling — not the geometry, which internal/polaralign covers
// exhaustively on its own.

// sweepSolver stands in for Siril. Each solve reports a telescope turned another step about the
// celestial pole: at constant declination, stepping right ascension is exactly that motion. So a
// session driven by this solver is a perfectly aligned mount, and must be told so.
//
// Which pole matters, and that is the point of the j2000Pole switch. The frames are swept about the
// pole OF DATE and then converted to J2000, because J2000 is what a plate solve speaks — precisely the
// conversion the fit has to undo. Sweeping about the J2000 pole instead describes a mount aimed nine
// arcminutes off, which is where the pole has moved since 2000.
type sweepSolver struct {
	mu       sync.Mutex
	baseRA   float64
	decDeg   float64
	stepDeg  float64
	calls    int
	failWith error
	// stalled makes every solve fail, to test what a passing cloud does.
	stalled bool
	// j2000Pole sweeps about the J2000 pole rather than today's, to prove precession is applied.
	j2000Pole bool
}

func newSweepSolver() *sweepSolver {
	return &sweepSolver{baseRA: 80, decDeg: 20, stepDeg: 20}
}

func (s *sweepSolver) Solve(_ context.Context, path string, _ platesolve.Hint) (platesolve.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWith != nil {
		return platesolve.Result{}, s.failWith
	}
	if s.stalled {
		return platesolve.Result{}, errors.New("not enough stars")
	}
	if _, err := os.Stat(path); err != nil {
		return platesolve.Result{}, err // the session must really have written a frame
	}
	// Hour angle increases as right ascension falls, so stepping RA down turns the telescope about the
	// pole — the motion a correctly aligned mount makes.
	ra, dec := s.baseRA-float64(s.calls)*s.stepDeg, s.decDeg
	s.calls++
	if !s.j2000Pole {
		// Swept about today's pole, then expressed in the J2000 a plate solve would report.
		ra, dec = astro.PrecessToJ2000(ra, dec, time.Now())
	}

	const scale = 1.06 / 3600
	wcs, ok := fits.NewTanWCS(ra, dec, 65, 65, [2][2]float64{{-scale, 0}, {0, scale}})
	if !ok {
		return platesolve.Result{}, errors.New("bad synthetic wcs")
	}
	return platesolve.Result{WCS: wcs, RADeg: ra, DecDeg: dec, ScaleArcsecPx: 1.06}, nil
}

func (s *sweepSolver) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *sweepSolver) stall(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stalled = v
}

// hold stops the sweep where it is. The measurement is the only part where the right-ascension axis is
// turned; during the adjustment the user has both hands on the altitude and azimuth bolts, and a
// telescope that kept swinging would be a different test entirely.
func (s *sweepSolver) hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// calls has already been advanced past the last frame it served, hence the minus one.
	if s.calls > 0 {
		s.baseRA -= float64(s.calls-1) * s.stepDeg
	}
	s.calls, s.stepDeg = 0, 0
}

func polarRig(t *testing.T) (*PolarSession, *sweepSolver) {
	t.Helper()
	runner, _ := testRig(t)
	solver := newSweepSolver()
	sess := NewPolarSession(runner.client, solver)
	t.Cleanup(sess.Stop)
	return sess, solver
}

// polarOpts runs the session with refraction OFF, which is what makes sweepSolver's perfect circle mean
// "a perfectly aligned mount".
//
// With refraction on it would not, and that is not a quirk of the test — it is the physics the option
// models. A mount whose axis is exactly on the pole sweeps a perfect circle MECHANICALLY, and the
// atmosphere then squashes it before the light reaches the sensor, so what a plate solve reports is not
// a circle at all. A solver that invents a perfect circle in catalogue coordinates is describing a
// mount that is genuinely a few arcminutes out, and the fit says so — see
// TestPolarSession_RefractionChangesTheAnswer.
func polarOpts() PolarOptions {
	return PolarOptions{
		Site:         polaralign.Site{LatDeg: 48.85, LonDeg: 2.35},
		ExposureUs:   20_000,
		FocalMM:      740,
		PixelUm:      3.8,
		NoRefraction: true,
	}
}

// The whole measurement, end to end: start, turn, turn, turn, and be told where the axis points. A
// telescope swept about the true pole is a correctly aligned mount, so the answer has to be "nothing
// to do" — which checks the session's plumbing and the fit's sign conventions in one go.
func TestPolarSession_MeasuresASweepAboutTheTruePole(t *testing.T) {
	sess, solver := polarRig(t)
	ctx := context.Background()

	require.NoError(t, sess.Start(ctx, polarOpts()))
	st := sess.Snapshot()
	assert.Equal(t, PolarMeasuring, st.Phase)
	assert.Equal(t, 1, st.Step)
	assert.Equal(t, defaultPoints, st.Points)
	assert.False(t, st.Busy)

	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx), "step %d", i)
	}

	st = sess.Snapshot()
	require.Equal(t, PolarSolved, st.Phase, "error: %s", st.Error)
	require.NotNil(t, st.Axis)
	require.NotNil(t, st.Correction)
	assert.Len(t, st.Samples, defaultPoints)
	assert.Equal(t, defaultPoints, solver.callCount())

	// Swept about the true pole, so the mount IS aligned.
	assert.Less(t, st.Correction.TotalArcmin, 1.0,
		"a sweep about the celestial pole must read as aligned, got %.2f′", st.Correction.TotalArcmin)
	assert.Equal(t, polaralign.QualityExcellent, st.Correction.Quality)
	assert.Equal(t, polaralign.MoveNone, st.Correction.AltMove)
	assert.Equal(t, polaralign.MoveNone, st.Correction.AzMove)
	assert.InDelta(t, 48.85, st.Axis.AltDeg, 0.05, "the axis should sit at the latitude")
	assert.InDelta(t, 3*20, st.Axis.ArcDeg, 1.0, "three twenty-degree steps")
	assert.Empty(t, st.Warnings)

	// And every sample carries what the UI needs to explain itself.
	for i, s := range st.Samples {
		assert.Equal(t, i+1, s.Index)
		assert.InDelta(t, 1.06, s.ScaleArcsecPx, 0.01)
		assert.False(t, s.At.IsZero(), "the fit works in hour angle; a sample without a time is useless")
	}
}

// Precession has to be applied, and this is what happens if it is not.
//
// A plate solve speaks J2000; local sidereal time speaks today. The pole has moved about nine
// arcminutes between the two, so a mount aimed perfectly at the J2000 pole is nine arcminutes out
// today — and a fit that forgot to convert would report those nine arcminutes as zero. Sweeping about
// the J2000 pole on purpose turns that into an assertion, with the expected answer computed from
// internal/astro rather than written down as a number that will quietly rot.
func TestPolarSession_AppliesPrecession(t *testing.T) {
	sess, solver := polarRig(t)
	solver.j2000Pole = true
	ctx := context.Background()

	require.NoError(t, sess.Start(ctx, polarOpts()))
	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx))
	}

	st := sess.Snapshot()
	require.NotNil(t, st.Correction, "error: %s", st.Error)

	// Where the J2000 pole sits in today's coordinates, i.e. how far a J2000-aimed mount really is out.
	ra, dec := astro.PrecessFromJ2000(0, 90, time.Now())
	_ = ra
	wantArcmin := (90 - dec) * 60
	assert.Greater(t, wantArcmin, 5.0, "sanity: precession since 2000 is several arcminutes")
	assert.InDelta(t, wantArcmin, st.Correction.TotalArcmin, 0.5,
		"a mount aimed at the J2000 pole must read as exactly that far out today")
}

// Refraction is on by default, and it is not decoration: over a sixty-degree arc it is worth several
// arcminutes — more than the whole error this feature exists to remove. The same frames measured with
// and without it must therefore disagree, visibly.
func TestPolarSession_RefractionChangesTheAnswer(t *testing.T) {
	run := func(noRefraction bool) float64 {
		sess, _ := polarRig(t)
		ctx := context.Background()
		opts := polarOpts()
		opts.NoRefraction = noRefraction
		require.NoError(t, sess.Start(ctx, opts))
		for i := 2; i <= defaultPoints; i++ {
			require.NoError(t, sess.Next(ctx))
		}
		st := sess.Snapshot()
		require.NotNil(t, st.Correction, "error: %s", st.Error)
		return st.Correction.TotalArcmin
	}

	assert.Less(t, run(true), 1.0, "without a refraction model the perfect circle is a perfect mount")
	assert.Greater(t, run(false), 2.0,
		"with one, the same catalogue positions describe a mount several arcminutes out")
}

// The adjust phase has to start from the measurement without asking the user to do anything else.
func TestPolarSession_AdjustPublishesATarget(t *testing.T) {
	sess, solver := polarRig(t)
	ctx := context.Background()
	require.NoError(t, sess.Start(ctx, polarOpts()))
	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx))
	}

	solver.hold()
	require.NoError(t, sess.Adjust(ctx))
	st := sess.Snapshot()
	require.Equal(t, PolarAdjusting, st.Phase)
	require.NotNil(t, st.Live)
	assert.False(t, st.Busy)
	// The mount is aligned, so the marker sits on the crosshairs and there is nothing left to do.
	assert.Less(t, st.Live.RemainingArcmin, 1.0)
	assert.Less(t, st.Live.Target.OffsetPx, 5.0)
	assert.False(t, st.Live.Suspect)

	require.NoError(t, sess.Refresh(ctx))
	assert.Equal(t, PolarAdjusting, sess.Snapshot().Phase)
}

// A cloud during the adjustment must not throw away a measurement that took four exposures to make.
func TestPolarSession_ACloudDuringAdjustmentKeepsTheSession(t *testing.T) {
	sess, solver := polarRig(t)
	ctx := context.Background()
	require.NoError(t, sess.Start(ctx, polarOpts()))
	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx))
	}
	solver.hold()
	require.NoError(t, sess.Adjust(ctx))
	good := sess.Snapshot()
	require.NotNil(t, good.Live)

	solver.stall(true)
	assert.Error(t, sess.Refresh(ctx))

	st := sess.Snapshot()
	assert.Equal(t, PolarAdjusting, st.Phase, "one unsolvable frame is weather, not a failure")
	assert.NotEmpty(t, st.Error)
	require.NotNil(t, st.Live)
	assert.Equal(t, good.Live.Target.X, st.Live.Target.X, "the last good marker stays on screen")

	solver.stall(false)
	require.NoError(t, sess.Refresh(ctx))
	assert.Empty(t, sess.Snapshot().Error)
}

// A solve that fails during the MEASUREMENT is different: there is nothing to fall back to.
func TestPolarSession_FailsWhenAFrameWillNotSolve(t *testing.T) {
	sess, solver := polarRig(t)
	ctx := context.Background()
	solver.failWith = errors.New("there are not enough stars picked in the image")

	err := sess.Start(ctx, polarOpts())
	require.Error(t, err)
	st := sess.Snapshot()
	assert.Equal(t, PolarFailed, st.Phase)
	assert.Contains(t, st.Error, "not enough stars")
	assert.False(t, st.Busy)
}

func TestPolarSession_RejectsOutOfOrderSteps(t *testing.T) {
	sess, _ := polarRig(t)
	ctx := context.Background()

	assert.ErrorIs(t, sess.Next(ctx), ErrPolarNotRunning)
	assert.ErrorIs(t, sess.Adjust(ctx), ErrPolarNotRunning)
	assert.ErrorIs(t, sess.Refresh(ctx), ErrPolarNotRunning)

	require.NoError(t, sess.Start(ctx, polarOpts()))
	assert.ErrorIs(t, sess.Adjust(ctx), ErrPolarNotRunning, "cannot adjust before the axis is known")
	assert.ErrorIs(t, sess.Refresh(ctx), ErrPolarNotRunning)
}

// Without a solver there is no measurement to be had, and saying so up front beats exposing four
// frames and then admitting it.
func TestPolarSession_RefusesWithoutASolver(t *testing.T) {
	runner, _ := testRig(t)
	sess := NewPolarSession(runner.client, nil)
	assert.ErrorIs(t, sess.Start(context.Background(), polarOpts()), ErrPolarNoSolver)
}

// Stop has to leave nothing behind: a night of restarted sessions would otherwise fill the work
// directory with solved frames nobody will ever look at.
func TestPolarSession_StopClearsItsFrames(t *testing.T) {
	sess, _ := polarRig(t)
	ctx := context.Background()
	opts := polarOpts()
	opts.ScratchDir = t.TempDir()
	require.NoError(t, sess.Start(ctx, opts))

	entries, err := os.ReadDir(opts.ScratchDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the session should have made one scratch directory")

	sess.Stop()
	assert.Equal(t, PolarIdle, sess.Snapshot().Phase)
	entries, err = os.ReadDir(opts.ScratchDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Stop must clear the frames it wrote")
}

// The panel renders from a stream, so every step has to publish.
func TestPolarSession_PublishesEveryStep(t *testing.T) {
	sess, _ := polarRig(t)
	ctx := context.Background()

	updates, unsubscribe := sess.Subscribe()

	var mu sync.Mutex
	phases := map[PolarPhase]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for st := range updates {
			mu.Lock()
			phases[st.Phase]++
			mu.Unlock()
		}
	}()

	require.NoError(t, sess.Start(ctx, polarOpts()))
	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx))
	}
	require.NoError(t, sess.Adjust(ctx))

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return phases[PolarMeasuring] > 0 && phases[PolarSolved] > 0 && phases[PolarAdjusting] > 0
	}, 5*time.Second, 20*time.Millisecond, "the stream missed a phase")

	unsubscribe()
	<-done
}
