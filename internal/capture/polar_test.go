package capture

import (
	"context"
	"errors"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	_ "github.com/verove-jordan/astronomy/internal/platesolve"
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
	mu sync.Mutex
	// The sweep is anchored to HOUR ANGLE rather than to a fixed right ascension, so the samples land
	// at the same altitudes whatever time of day the suite runs. Refraction depends on altitude, so a
	// right-ascension-anchored sweep would make the refraction assertions drift with the clock — a
	// test that passes in the morning and fails at night is worse than no test.
	lonDeg   float64
	haDeg    float64 // hour angle of the pointing the NEXT solve will report
	decDeg   float64
	stepDeg  float64
	calls    int
	failWith error
	// stalled makes every solve fail, to test what a passing cloud does.
	stalled bool
	// j2000Pole sweeps about the J2000 pole rather than today's, to prove precession is applied.
	j2000Pole bool
	// nearPole aims a fixed offset from the pole instead of sweeping, for the one-frame mode.
	nearPole *[2]float64
	// hints records what each solve was told to look at, so a test can assert the solver is given
	// somewhere to start rather than nothing at all.
	hints []platesolve.Hint
}

func newSweepSolver() *sweepSolver {
	// Starting thirty degrees east of the meridian and sweeping sixty degrees west keeps every frame
	// high in the sky and well clear of the horizon, where the refraction model gets shaky.
	return &sweepSolver{lonDeg: 2.35, haDeg: -30, decDeg: 20, stepDeg: 20}
}

func (s *sweepSolver) Solve(_ context.Context, path string, hint platesolve.Hint) (platesolve.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hints = append(s.hints, hint)
	if s.failWith != nil {
		return platesolve.Result{}, s.failWith
	}
	if s.stalled {
		return platesolve.Result{}, errors.New("not enough stars")
	}
	if _, err := os.Stat(path); err != nil {
		return platesolve.Result{}, err // the session must really have written a frame
	}
	now := time.Now()
	if s.nearPole != nil {
		return s.poleFrameLocked(now)
	}
	// Stepping hour angle at constant declination turns the telescope about the pole — the motion a
	// correctly aligned mount makes.
	ra, dec := norm360(astro.LST(now, s.lonDeg)-s.haDeg), s.decDeg
	s.haDeg += s.stepDeg
	s.calls++
	if !s.j2000Pole {
		// Swept about today's pole, then expressed in the J2000 a plate solve would report.
		ra, dec = astro.PrecessToJ2000(ra, dec, now)
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

// aimNearPole parks the synthetic telescope a known distance from the pole and stops it sweeping,
// which is the situation the one-frame mode is for: declination at its index, pointing up the axis.
func (s *sweepSolver) aimNearPole(altOffsetDeg, azOffsetDeg float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nearPole = &[2]float64{altOffsetDeg, azOffsetDeg}
	s.stepDeg = 0
}

// hold stops the sweep where it is. The measurement is the only part where the right-ascension axis is
// turned; during the adjustment the user has both hands on the altitude and azimuth bolts, and a
// telescope that kept swinging would be a different test entirely.
func (s *sweepSolver) hold() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.haDeg -= s.stepDeg // rewind the step queued for the frame that never came
	s.stepDeg = 0
}

func (s *sweepSolver) recordedHints() []platesolve.Hint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]platesolve.Hint(nil), s.hints...)
}

// The FIRST frame of a session has no previous sample to hint from, so its pointing has to come from
// the mount. Measured against Siril 1.4: given neither coordinates nor a header carrying them it
// refuses outright — "Cannot plate solve, no target coordinates passed and image header doesn't
// contain any either" — so the wizard could never get past step 1, on either entry point.
func TestPolarSession_FirstSolveIsToldWhereToLook(t *testing.T) {
	runner, _ := testRig(t)
	ctx := context.Background()
	mount, err := runner.client.do2(ctx) // connect the simulated mount
	require.NoError(t, err)
	require.True(t, mount.Connected)

	solver := newSweepSolver()
	sess := NewPolarSession(runner.client, solver)
	t.Cleanup(sess.Stop)

	require.NoError(t, sess.Start(ctx, polarOpts()))

	hints := solver.recordedHints()
	require.NotEmpty(t, hints, "the session must have solved a frame")
	first := hints[0]
	assert.True(t, first.HasHint, "the first solve must be given the mount's pointing")
	assert.InDelta(t, mount.Mount.RADeg, first.RADeg, 1.0)
	assert.InDelta(t, mount.Mount.DecDeg, first.DecDeg, 1.0)
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

// Refraction is on by default, and it is not decoration.
//
// Measured here, on a sixty-degree arc at declination 20° with the frames between 52° and 61° above
// the horizon: leaving it out shifts the answer by about three quarters of an arcminute. That is most
// of the one-arcminute budget the whole feature exists to get inside — and it grows quickly lower in
// the sky, where the arc has to go when the target does.
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

	assert.Less(t, run(true), 0.05,
		"without a refraction model the perfect circle is a perfect mount")
	bias := run(false)
	assert.Greater(t, bias, 0.4, "ignoring refraction has to cost a visible part of an arcminute")
	assert.Less(t, bias, 2.0, "and it is a bias of that order, not a blow-up")
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

// norm360 wraps an angle into [0,360).
func norm360(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	return d
}

// The one-frame mode: point near the pole, take a single frame, and be told the error — with the
// assumption it rests on attached, and clearly distinguished from a measured answer.
func TestPolarSession_RoughAnswersFromOneFrame(t *testing.T) {
	sess, solver := polarRig(t)
	ctx := context.Background()

	// Aim half a degree above the pole and a quarter east of it, so there is a known answer to find.
	solver.aimNearPole(0.5, 0.25)

	require.NoError(t, sess.Rough(ctx, polarOpts()))

	st := sess.Snapshot()
	require.Equal(t, PolarSolved, st.Phase, "error: %s", st.Error)
	require.NotNil(t, st.Correction)
	assert.Equal(t, PolarRough, st.Mode)
	assert.Equal(t, 1, solver.callCount(), "one frame, not four")

	assert.InDelta(t, 0.5, st.Correction.AltErrorDeg, 0.02)
	assert.InDelta(t, 0.25, st.Correction.AzKnobDeg, 0.02)
	assert.Equal(t, polaralign.MoveLower, st.Correction.AltMove)
	assert.Equal(t, polaralign.MoveWest, st.Correction.AzMove)

	// It must say what it assumed, and not claim measured-class precision.
	assert.Contains(t, st.Warnings, polaralign.WarnAssumedOnAxis)
	assert.Greater(t, st.Axis.SigmaArcsec, 600.0)

	// And the pole is on screen for the user to steer toward.
	require.NotNil(t, st.Pole)
	assert.Equal(t, "Polaris", st.Pole.StarName)
	assert.InDelta(t, 0.5*60, st.Pole.Pole.OffsetArcmin, 5)

	// The adjust loop is shared: the marker it drives toward is the pole.
	solver.hold()
	require.NoError(t, sess.Adjust(ctx))
	live := sess.Snapshot().Live
	require.NotNil(t, live)
	assert.InDelta(t, st.Pole.Pole.X, live.Target.X, 2)
}

// A measured session says so, so the UI can present the two differently — they are two orders of
// magnitude apart in what they are worth.
func TestPolarSession_MeasuredAndRoughAreDistinguishable(t *testing.T) {
	sess, _ := polarRig(t)
	ctx := context.Background()

	require.NoError(t, sess.Start(ctx, polarOpts()))
	for i := 2; i <= defaultPoints; i++ {
		require.NoError(t, sess.Next(ctx))
	}

	st := sess.Snapshot()
	assert.Equal(t, PolarMeasured, st.Mode)
	assert.NotContains(t, st.Warnings, polaralign.WarnAssumedOnAxis)
	assert.Less(t, st.Axis.SigmaArcsec, 60.0, "a measured answer is worth far more than a rough one")
}

// poleFrameLocked reports a telescope aimed a known offset from the pole. Caller holds the lock.
func (s *sweepSolver) poleFrameLocked(now time.Time) (platesolve.Result, error) {
	s.calls++
	// Raise the pointing above the pole and carry it east, in the horizon frame, then say where that
	// lands on the sky — the same construction the geometry package tests use.
	alt := 48.85 + s.nearPole[0]
	az := s.nearPole[1]
	ra, dec := polaralign.SkyFromAltAz(alt, az,
		polaralign.Site{LatDeg: 48.85, LonDeg: s.lonDeg}, now, polaralign.FitOptions{})

	const scale = 4.0 / 3600 // a wide field, so the pole and Polaris both fit
	wcs, ok := fits.NewTanWCS(ra, dec, 65, 65, [2][2]float64{{-scale, 0}, {0, scale}})
	if !ok {
		return platesolve.Result{}, errors.New("bad synthetic wcs")
	}
	return platesolve.Result{WCS: wcs, RADeg: ra, DecDeg: dec, ScaleArcsecPx: 4}, nil
}

// The seed decides where Siril looks, and its near solver searches only about 10° around it — so
// "any seed beats none" is false. A seed from a mount that has not been aligned is a search of the
// wrong sky, and it is what produced "the near solver could not find a solution over the search
// radius (10.0 deg)" on a telescope pointed straight at Polaris: the mount answered Dec 0.1° for a
// tube looking 89° away, because an unaligned Celestron reports against a home nobody established.
func TestUsableMountSeed(t *testing.T) {
	tests := []struct {
		name string
		st   MountState
		want bool
	}{
		{
			name: "an aligned mount knows where it points",
			st:   MountState{Connected: true, Mount: device.MountState{Aligned: true, RADeg: 37.95, DecDeg: 89.26}},
			want: true,
		},
		{
			name: "an unaligned one answers anyway, and the answer is fiction",
			st:   MountState{Connected: true, Mount: device.MountState{Aligned: false, RADeg: 179.66, DecDeg: 0.15}},
			want: false,
		},
		{
			name: "no mount at all",
			st:   MountState{Connected: false, Mount: device.MountState{Aligned: true, RADeg: 10, DecDeg: 20}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ra, dec, ok := usableMountSeed(tt.st)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Equal(t, tt.st.Mount.RADeg, ra)
				assert.Equal(t, tt.st.Mount.DecDeg, dec)
			}
		})
	}
}

// An unusable mount reply must leave the seed alone rather than replace it. The per-frame refresh
// runs after "Find the pole" has seeded the pole, so a rule applied only at Start would be undone on
// the very next frame.
func TestPolarSession_AnUnalignedMountNeverOverwritesTheSeed(t *testing.T) {
	sess, _ := polarRig(t)
	sess.opts = polarOpts()
	sess.seedCelestialPole()

	sess.takeMountSeedLocked(MountState{
		Connected: true,
		Mount:     device.MountState{Aligned: false, RADeg: 179.66, DecDeg: 0.15},
	})

	hint, from := sess.solveHintLocked()
	require.True(t, hint.HasHint)
	assert.InDelta(t, 90.0, hint.DecDeg, 1e-9, "the pole seed must survive an unaligned mount")
	assert.Contains(t, from, "celestial pole")
}

// "Find the pole" asserts the tube is looking down the right ascension axis, so the pole is the one
// seed it can always supply — with no mount, no alignment and no blind solver installed. A ~10°
// search around it covers every right ascension, because they all meet there.
func TestPolarSession_SeedCelestialPoleFollowsTheHemisphere(t *testing.T) {
	for _, tt := range []struct {
		name   string
		latDeg float64
		want   float64
	}{
		{"northern site", 48.85, 90},
		{"southern site", -33.87, -90},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sess, _ := polarRig(t)
			sess.opts = PolarOptions{Site: polaralign.Site{LatDeg: tt.latDeg}}
			sess.seedCelestialPole()

			hint, from := sess.solveHintLocked()
			require.True(t, hint.HasHint)
			assert.Equal(t, tt.want, hint.DecDeg)
			assert.Contains(t, from, "celestial pole")
		})
	}
}

// A solved frame outranks every seed: it is measured rather than asserted.
func TestPolarSession_ASolvedFrameOutranksTheSeed(t *testing.T) {
	sess, _ := polarRig(t)
	sess.opts = polarOpts()
	sess.seedCelestialPole()
	sess.samples = []polaralign.Sample{{RADeg: 120.5, DecDeg: 31.25, At: time.Now()}}

	hint, from := sess.solveHintLocked()
	require.True(t, hint.HasHint)
	assert.Equal(t, 120.5, hint.RADeg)
	assert.Equal(t, 31.25, hint.DecDeg)
	assert.Contains(t, from, "previous solved frame")
}

// Siril can only report that it found nothing within its radius. Which of the several reasons it was
// is exactly what the user cannot see, so the session says it.
func TestExplainSolveFailure_NamesTheReasonSirilCannot(t *testing.T) {
	base := errors.New("Siril near solver could not find a solution over the search radius (10.0 deg)")

	unseeded := explainSolveFailure(base, platesolve.Hint{FocalMM: 740, PixelUm: 3.8}, "")
	assert.Contains(t, unseeded.Error(), "the mount is not aligned")
	assert.Contains(t, unseeded.Error(), "Find the pole")

	seeded := explainSolveFailure(base,
		platesolve.Hint{HasHint: true, RADeg: 37.95, DecDeg: 89.26, FocalMM: 740, PixelUm: 3.8},
		"the mount's reported position")
	assert.Contains(t, seeded.Error(), "the mount's reported position")
	assert.Contains(t, seeded.Error(), "740 mm")
}

// A stopped drive must be reported as a warning CODE the panel can translate, not as prose — and it
// must survive the fit publishing its own warnings, because settle replaces that list.
func TestPolarSession_WarnsWhenTheDriveIsStopped(t *testing.T) {
	sess, _ := polarRig(t)
	sess.opts = polarOpts()
	sess.setupWarnings = []string{warnDriveStopped}

	sess.settle(polaralign.Frame{At: time.Now()}, polaralign.Axis{Warnings: []string{"weak_arc"}}, PolarMeasured)

	assert.Equal(t, []string{warnDriveStopped, "weak_arc"}, sess.Snapshot().Warnings)
}
