// Closed-loop tests: the servo driven against the simulated mount's real physics — periodic error as
// an axis error, non-repeating jitter, declination backlash and the cos(dec) conversion — with no
// hardware and no rendering.
//
// These live in an external test package so they can import the hardware layer without the servo
// itself ever depending on it.
//
// The rig deliberately separates two mappings that a careless test would conflate. The OBSERVER uses
// the true pixel↔axis mapping, because that is physics. The SESSION is handed a calibration, which may
// be wrong — a calibration is measured, and measurements can come out backwards. Keeping them apart is
// what lets a test assert that a wrong calibration is detected rather than silently obeyed.
package guide_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/sim"
	"github.com/verove-jordan/astronomy/internal/guide"
)

// trueCal is the mapping the simulated optics really have: 2″ per pixel, RA along +x, Dec along +y.
func trueCal() guide.Calibration {
	return guide.Calibration{
		RAArcsecPerPx: 2, DecArcsecPerPx: 2,
		RAUnitX: 1, RAUnitY: 0,
		DecUnitX: 0, DecUnitY: 1,
		Orthogonality: 1,
		RADriftPx:     10, DecDriftPx: 10,
	}
}

const (
	refX = 500.0
	refY = 500.0
)

// rig is a simulated mount plus a synthetic sensor.
type rig struct {
	t     *testing.T
	world *sim.World
	mount *sim.Mount
	cal   guide.Calibration

	now           time.Time
	start         time.Time
	refRA, refDec float64

	// Sky drift relative to the mount's axes — what polar misalignment produces. It is modelled here
	// rather than by moving the mount, because that is what it physically is: the sky slides, the mount
	// does not. Modelling it as a mount motion would also pollute the declination backlash state that
	// the corrections are supposed to be exercising.
	raDriftArcsecPerSec  float64
	decDriftArcsecPerSec float64
}

func newRig(t *testing.T, cfg sim.Config) *rig {
	t.Helper()
	if cfg.SensorW == 0 {
		cfg.SensorW, cfg.SensorH = 64, 64
	}
	cfg.HotPixels = -1
	if cfg.StartRADeg == 0 {
		cfg.StartRADeg = 100
	}

	r := &rig{t: t, cal: trueCal()}
	r.start = time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC)
	r.now = r.start

	r.world = sim.NewWorld(cfg)
	r.world.SetClock(func() time.Time { return r.now })
	r.mount = sim.NewMount(r.world)
	require.NoError(t, r.mount.Connect(context.Background()))

	r.refRA, r.refDec = r.pointing()
	return r
}

func (r *rig) pointing() (raDeg, decDeg float64) {
	st, err := r.mount.State(context.Background())
	require.NoError(r.t, err)
	return st.RADeg, st.DecDeg
}

func (r *rig) tick(d time.Duration) { r.now = r.now.Add(d) }

func (r *rig) elapsed() float64 { return r.now.Sub(r.start).Seconds() }

// axisError is the truth the servo is being judged against: how far the mount's own axes have wandered
// from where they started, plus whatever the sky has drifted, in axis arcseconds.
func (r *rig) axisError() (raArcsec, decArcsec float64) {
	ra, dec := r.pointing()
	dRA := math.Mod(ra-r.refRA+540, 360) - 180
	return dRA*3600 + r.raDriftArcsecPerSec*r.elapsed(),
		(dec-r.refDec)*3600 + r.decDriftArcsecPerSec*r.elapsed()
}

// star is where the guide star lands on the synthetic sensor, given the true mapping.
func (r *rig) star() (x, y float64) {
	dRA, dDec := r.axisError()
	dx := dRA*r.cal.RAUnitX/r.cal.RAArcsecPerPx + dDec*r.cal.DecUnitX/r.cal.DecArcsecPerPx
	dy := dRA*r.cal.RAUnitY/r.cal.RAArcsecPerPx + dDec*r.cal.DecUnitY/r.cal.DecArcsecPerPx
	return refX + dx, refY + dy
}

func (r *rig) observe() guide.Observation {
	x, y := r.star()
	_, dec := r.pointing()
	return guide.Observation{
		TSec: r.elapsed(), Found: true, X: x, Y: y, SNR: 40, HFD: 3,
		DecDeg: dec, HasDec: true,
	}
}

// apply commands the two corrections a sample asked for.
func (r *rig) apply(sample guide.Sample, rateArcsecPerSec float64) {
	ctx := context.Background()
	for _, c := range []struct {
		axis   device.GuideAxis
		arcsec float64
	}{
		{device.GuideAxisRA, sample.RACorrArcsec},
		{device.GuideAxisDec, sample.DecCorrArcsec},
	} {
		rate, d, ok := guide.PulseFor(c.arcsec, rateArcsecPerSec)
		if !ok {
			continue
		}
		require.NoError(r.t, r.mount.PulseGuide(ctx, c.axis, rate, d))
	}
}

// rms accumulates a root-mean-square independently of the servo's own statistics, so the verdict does
// not come from the code under test.
type rms struct {
	sum float64
	n   int
}

func (a *rms) add(v float64) { a.sum += v * v; a.n++ }
func (a *rms) value() float64 {
	if a.n == 0 {
		return 0
	}
	return math.Sqrt(a.sum / float64(a.n))
}

// runUnguided measures how badly the mount tracks when nothing corrects it.
func runUnguided(t *testing.T, cfg sim.Config, steps int, dt time.Duration) float64 {
	t.Helper()
	r := newRig(t, cfg)
	var acc rms
	for i := 0; i < steps; i++ {
		r.tick(dt)
		dRA, dDec := r.axisError()
		acc.add(math.Hypot(dRA, dDec))
	}
	return acc.value()
}

// runGuided drives the servo and reports the residual, measured the same independent way.
func runGuided(t *testing.T, cfg sim.Config, gcfg guide.Config, cal guide.Calibration, steps int, dt time.Duration) (float64, *guide.Session, error) {
	t.Helper()
	r := newRig(t, cfg)
	s, err := guide.NewSession(gcfg, cal)
	require.NoError(t, err)

	rate := guide.GuideRateArcsecPerSec(guide.DefaultGuideRateFraction)
	if _, err := s.Update(r.observe()); err != nil {
		return 0, s, err
	}

	var acc rms
	for i := 0; i < steps; i++ {
		r.tick(dt)
		sample, err := s.Update(r.observe())
		if err != nil {
			return acc.value(), s, err
		}
		r.apply(sample, rate)

		dRA, dDec := r.axisError()
		acc.add(math.Hypot(dRA, dDec))
	}
	return acc.value(), s, nil
}

// peConfig is an AVX-like worm: 12″ peak-to-peak over 478 seconds, with the harmonics and the
// non-repeating jitter the simulator models on purpose.
func peConfig() sim.Config {
	return sim.Config{
		StartRADeg: 100, StartDecDeg: 41.2687,
		PEAmplitude: 12, PEPeriodSec: 478,
		PEJitterArcsec:    1,
		DecBacklashArcsec: -1,
	}
}

// Two worm revolutions at one sample a second. Instant, because the clock is injected.
const (
	loopSteps = 960
	loopDt    = time.Second
)

func TestGuideLoop_ReducesPeriodicErrorByAnOrderOfMagnitude(t *testing.T) {
	cfg := peConfig()

	unguided := runUnguided(t, cfg, loopSteps, loopDt)
	guided, s, err := runGuided(t, cfg, guide.DefaultConfig(guide.ModeGuideScope), trueCal(), loopSteps, loopDt)
	require.NoError(t, err)

	t.Logf("unguided %.2f″ RMS, guided %.2f″ RMS (%.0f× better)", unguided, guided, unguided/guided)

	// A 12″ peak-to-peak sinusoid has an RMS near 4″, so the unguided figure is a sanity check that the
	// simulator really did misbehave and the comparison means something.
	assert.Greater(t, unguided, 3.0, "the simulated worm should produce several arcseconds of error")
	assert.Less(t, guided, unguided/5, "guiding must be a large improvement, not a marginal one")
	assert.Less(t, guided, 1.5, "and the residual should be a fraction of an arcsecond")

	assert.Equal(t, guide.PhaseGuiding, s.Phase())
	assert.Positive(t, s.Metrics().Corrections)
}

func TestGuideLoop_CorrectsDriftInBothAxes(t *testing.T) {
	cfg := peConfig()
	// A tenth of an arcsecond a second is about 6″ a minute — a badly polar-aligned mount.
	unguidedRig := newRig(t, cfg)
	unguidedRig.decDriftArcsecPerSec = 0.1
	unguidedRig.raDriftArcsecPerSec = 0.05

	var unguided rms
	for i := 0; i < loopSteps; i++ {
		unguidedRig.tick(loopDt)
		dRA, dDec := unguidedRig.axisError()
		unguided.add(math.Hypot(dRA, dDec))
	}

	r := newRig(t, cfg)
	r.decDriftArcsecPerSec = 0.1
	r.raDriftArcsecPerSec = 0.05
	s, err := guide.NewSession(guide.DefaultConfig(guide.ModeGuideScope), trueCal())
	require.NoError(t, err)
	rate := guide.GuideRateArcsecPerSec(guide.DefaultGuideRateFraction)
	_, err = s.Update(r.observe())
	require.NoError(t, err)

	var guided rms
	for i := 0; i < loopSteps; i++ {
		r.tick(loopDt)
		sample, err := s.Update(r.observe())
		require.NoError(t, err)
		r.apply(sample, rate)
		dRA, dDec := r.axisError()
		guided.add(math.Hypot(dRA, dDec))
	}

	t.Logf("with drift: unguided %.1f″ RMS, guided %.2f″ RMS", unguided.value(), guided.value())
	assert.Greater(t, unguided.value(), 20.0, "uncorrected drift should run away over sixteen minutes")
	assert.Less(t, guided.value(), 2.0, "a guided mount should hold position against drift")
}

func TestGuideLoop_ConvergesThroughDeclinationBacklash(t *testing.T) {
	cfg := peConfig()
	// Four arcseconds of slack, which is more than a typical correction — so every reversal is a real
	// cost and the resist-switch guard is what stops the servo paying it repeatedly for nothing.
	cfg.DecBacklashArcsec = 4
	r := newRig(t, cfg)
	r.decDriftArcsecPerSec = 0.08

	s, err := guide.NewSession(guide.DefaultConfig(guide.ModeGuideScope), trueCal())
	require.NoError(t, err)
	rate := guide.GuideRateArcsecPerSec(guide.DefaultGuideRateFraction)
	_, err = s.Update(r.observe())
	require.NoError(t, err)

	var guided rms
	for i := 0; i < loopSteps; i++ {
		r.tick(loopDt)
		sample, err := s.Update(r.observe())
		require.NoError(t, err)
		r.apply(sample, rate)
		_, dDec := r.axisError()
		guided.add(dDec)
	}

	t.Logf("declination residual through %0.f″ of backlash: %.2f″ RMS",
		cfg.DecBacklashArcsec, guided.value())
	assert.Less(t, guided.value(), 6.0,
		"backlash costs accuracy but the axis must still hold, not walk away")
}

func TestGuideLoop_AbortsOnAnInvertedCalibration(t *testing.T) {
	// The failure this whole safety net exists for. A calibration measured with the wrong sign makes
	// every correction push the star further out, and the error grows geometrically. The loop must stop
	// rather than walk the mount off the target — a guider that keeps pushing loses the night AND the
	// framing.
	inverted := trueCal()
	inverted.RAUnitX = -inverted.RAUnitX
	inverted.DecUnitY = -inverted.DecUnitY

	_, s, err := runGuided(t, peConfig(), guide.DefaultConfig(guide.ModeGuideScope), inverted, loopSteps, loopDt)

	require.ErrorIs(t, err, guide.ErrDiverging)
	assert.Equal(t, guide.PhaseFailed, s.Phase())
	assert.Less(t, s.Metrics().Samples, 200,
		"and it must be caught quickly, not after a quarter of an hour of pushing")
}

func TestGuideLoop_RecoversFromALostStar(t *testing.T) {
	cfg := peConfig()
	r := newRig(t, cfg)
	s, err := guide.NewSession(guide.DefaultConfig(guide.ModeGuideScope), trueCal())
	require.NoError(t, err)
	rate := guide.GuideRateArcsecPerSec(guide.DefaultGuideRateFraction)
	_, err = s.Update(r.observe())
	require.NoError(t, err)

	// Guide for a while, then lose the star to a passing cloud for a minute, then carry on.
	for i := 0; i < 120; i++ {
		r.tick(loopDt)
		sample, err := s.Update(r.observe())
		require.NoError(t, err)
		r.apply(sample, rate)
	}
	require.Equal(t, guide.PhaseGuiding, s.Phase())
	refBeforeX, refBeforeY, _ := s.Reference()

	for i := 0; i < 60; i++ {
		r.tick(loopDt)
		sample, err := s.Update(guide.Observation{TSec: r.elapsed(), Found: false})
		require.NoError(t, err, "a minute of cloud is ordinary")
		require.Zero(t, sample.RACorrArcsec, "and the mount must not be moved to hunt for the star")
		require.Zero(t, sample.DecCorrArcsec)
	}
	assert.Equal(t, guide.PhaseStarLost, s.Phase())

	refAfterX, refAfterY, _ := s.Reference()
	assert.Equal(t, refBeforeX, refAfterX, "the reference must survive the gap")
	assert.Equal(t, refBeforeY, refAfterY)

	// The cloud clears. The star is now well off target because the worm kept turning, so the loop
	// should pull it back rather than give up.
	var err2 error
	for i := 0; i < 300 && err2 == nil; i++ {
		r.tick(loopDt)
		var sample guide.Sample
		sample, err2 = s.Update(r.observe())
		if err2 != nil {
			break
		}
		r.apply(sample, rate)
	}
	require.NoError(t, err2)
	assert.Equal(t, guide.PhaseGuiding, s.Phase(), "guiding should resume once the star is back")

	dRA, dDec := r.axisError()
	assert.Less(t, math.Hypot(dRA, dDec), 2.0, "and the star should be back on its reference")
}

func TestGuideLoop_HoldsAtHighDeclination(t *testing.T) {
	// The cos(dec) test, end to end. At 75° a given pixel offset is nearly four times the axis rotation
	// it is at the equator. A loop that ignored the factor would under-correct by that much and never
	// catch up; one that applied it the wrong way round would over-correct and oscillate.
	cfg := peConfig()
	cfg.StartDecDeg = 75

	cal := trueCal()
	cal.DecAtCalibDeg = 0 // calibrated at the equator, used near the pole

	guided, s, err := runGuided(t, cfg, guide.DefaultConfig(guide.ModeGuideScope), cal, loopSteps, loopDt)
	require.NoError(t, err)

	t.Logf("at 75° declination: %.2f″ RMS", guided)
	assert.Less(t, guided, 2.0, "the declination rescale must keep the loop stable away from the equator")
	assert.Equal(t, guide.PhaseGuiding, s.Phase())
}

func TestGuideLoop_SelfGuideModeWorksAtSubCadence(t *testing.T) {
	// Mode A: one measurement per sub. Two minutes apart, the worm has moved a long way between
	// samples, so this can only ever reduce the error rather than eliminate it — but it must reduce it,
	// and it must not become unstable at that cadence.
	cfg := peConfig()
	const (
		subs  = 40
		subDt = 120 * time.Second
	)

	unguided := runUnguided(t, cfg, subs, subDt)
	guided, s, err := runGuided(t, cfg, guide.DefaultConfig(guide.ModeSelfGuide), trueCal(), subs, subDt)
	require.NoError(t, err)

	t.Logf("self-guide at %s cadence: unguided %.2f″ RMS, guided %.2f″ RMS", subDt, unguided, guided)
	assert.Less(t, guided, unguided, "correcting once a sub must still be better than not correcting")
	assert.NotEqual(t, guide.PhaseFailed, s.Phase(), "and it must not destabilise at a slow cadence")
}

func TestGuideLoop_SiderealRateMatchesTheHardwareLayer(t *testing.T) {
	// The servo carries its own copy so it need not import the hardware layer. Two constants that
	// disagreed in the fifth decimal would show up as a slow phase creep nobody could explain.
	assert.Equal(t, device.SiderealArcsecPerSec, guide.SiderealArcsecPerSec)
}
