package guide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plainConfig removes the servo's softening so a session test asserts on session policy rather than on
// axis tuning, which axis_test.go already covers.
func plainConfig(mode Mode) Config {
	c := DefaultConfig(mode)
	plain := AxisConfig{Aggressiveness: 1, MinMoveArcsec: 0.1, MaxMoveArcsec: 1000}
	c.RA, c.Dec = plain, plain
	return c
}

// startedSession returns a guiding session whose reference is already set at (100, 100).
func startedSession(t *testing.T, cfg Config) *Session {
	t.Helper()
	s, err := NewSession(cfg, squareCal())
	require.NoError(t, err)
	_, err = s.Update(Observation{TSec: 0, Found: true, X: 100, Y: 100})
	require.NoError(t, err)
	return s
}

func TestNewSession_RequiresCalibrationWhenGuiding(t *testing.T) {
	for _, mode := range []Mode{ModeSelfGuide, ModeGuideScope} {
		t.Run(string(mode), func(t *testing.T) {
			_, err := NewSession(DefaultConfig(mode), Calibration{})
			require.ErrorIs(t, err, ErrNotCalibrated)
		})
	}

	// Watching without steering needs no calibration — that is how you decide whether to trust one.
	_, err := NewSession(DefaultConfig(ModeOff), Calibration{})
	assert.NoError(t, err)
}

func TestNewSession_RejectsAnUnknownMode(t *testing.T) {
	_, err := NewSession(Config{Mode: "aggressive"}, squareCal())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown guide mode")
}

func TestSession_FirstGoodFrameBecomesTheReference(t *testing.T) {
	s := startedSession(t, plainConfig(ModeGuideScope))

	x, y, ok := s.Reference()
	require.True(t, ok)
	assert.Equal(t, 100.0, x)
	assert.Equal(t, 100.0, y)
	assert.Equal(t, PhaseSettling, s.Phase())

	// The reference frame has zero error by definition, so it must not be averaged into the statistics.
	assert.Zero(t, s.Metrics().RMSTotalArcsec)
	assert.Equal(t, 1, s.Metrics().Valid)
}

func TestSession_ProducesCorrectionsOppositeTheOffset(t *testing.T) {
	s := startedSession(t, plainConfig(ModeGuideScope))

	// 3 px right and 2 px up, at 2″ per pixel on both axes.
	sample, err := s.Update(Observation{TSec: 1, Found: true, X: 103, Y: 98})
	require.NoError(t, err)

	assert.InDelta(t, 3, sample.DX, 1e-9)
	assert.InDelta(t, -2, sample.DY, 1e-9)
	assert.InDelta(t, 6, sample.RAErrArcsec, 1e-9)
	assert.InDelta(t, -4, sample.DecErrArcsec, 1e-9)
	assert.InDelta(t, -6, sample.RACorrArcsec, 1e-9)
	assert.InDelta(t, 4, sample.DecCorrArcsec, 1e-9)
}

func TestSession_OffModeMeasuresWithoutCorrecting(t *testing.T) {
	s := startedSession(t, plainConfig(ModeOff))

	sample, err := s.Update(Observation{TSec: 1, Found: true, X: 110, Y: 100})
	require.NoError(t, err)

	assert.InDelta(t, 20, sample.RAErrArcsec, 1e-9, "the error is still measured")
	assert.Zero(t, sample.RACorrArcsec, "but nothing is commanded")
	assert.Zero(t, sample.DecCorrArcsec)
	assert.Zero(t, s.Metrics().Corrections)
}

func TestSession_OffModeWorksWithoutACalibration(t *testing.T) {
	s, err := NewSession(DefaultConfig(ModeOff), Calibration{})
	require.NoError(t, err)
	_, err = s.Update(Observation{TSec: 0, Found: true, X: 100, Y: 100})
	require.NoError(t, err)

	sample, err := s.Update(Observation{TSec: 1, Found: true, X: 107, Y: 100})
	require.NoError(t, err, "an uncalibrated watch-only session must not fail")
	assert.InDelta(t, 7, sample.DX, 1e-9, "pixel offsets are still recorded")
	assert.Zero(t, sample.RAErrArcsec, "there is no mapping to arcseconds yet")
}

func TestSession_SettlesAfterConsecutiveGoodSamples(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.SettleArcsec = 1
	cfg.SettleSamples = 3
	s := startedSession(t, cfg)

	// 0.2 px is 0.4″, comfortably inside the settle threshold.
	for i := 1; i <= 2; i++ {
		_, err := s.Update(Observation{TSec: float64(i), Found: true, X: 100.2, Y: 100})
		require.NoError(t, err)
		assert.False(t, s.Settled(), "one or two quiet samples is not settled")
	}

	_, err := s.Update(Observation{TSec: 3, Found: true, X: 100.2, Y: 100})
	require.NoError(t, err)
	assert.True(t, s.Settled())
	assert.Equal(t, PhaseGuiding, s.Phase())
}

func TestSession_SettleRunResetsOnALargeExcursion(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.SettleArcsec = 1
	cfg.SettleSamples = 3
	s := startedSession(t, cfg)

	_, _ = s.Update(Observation{TSec: 1, Found: true, X: 100.2, Y: 100})
	_, _ = s.Update(Observation{TSec: 2, Found: true, X: 100.2, Y: 100})
	// A star crossing the target on its way past is not a settled star.
	_, _ = s.Update(Observation{TSec: 3, Found: true, X: 104, Y: 100})
	_, _ = s.Update(Observation{TSec: 4, Found: true, X: 100.2, Y: 100})
	_, _ = s.Update(Observation{TSec: 5, Found: true, X: 100.2, Y: 100})

	assert.False(t, s.Settled(), "the settle run must restart after the excursion")
}

func TestSession_StarLostKeepsTheReferenceAndRecovers(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.MaxLostFrames = 5
	s := startedSession(t, cfg)

	for i := 1; i <= 3; i++ {
		sample, err := s.Update(Observation{TSec: float64(i), Found: false})
		require.NoError(t, err, "a few starless frames are ordinary, not a failure")
		assert.False(t, sample.Valid)
		assert.Zero(t, sample.RACorrArcsec, "the mount must never be moved to hunt for a lost star")
		assert.Zero(t, sample.DecCorrArcsec)
	}
	assert.Equal(t, PhaseStarLost, s.Phase())

	x, y, _ := s.Reference()
	assert.Equal(t, 100.0, x, "the reference is what the whole run is measured against; it must survive")
	assert.Equal(t, 100.0, y)

	_, err := s.Update(Observation{TSec: 4, Found: true, X: 100, Y: 100})
	require.NoError(t, err)
	assert.Equal(t, PhaseSettling, s.Phase())
	assert.Zero(t, s.Metrics().LostRun)
	assert.Equal(t, 3, s.Metrics().Lost, "the gap stays in the record")
}

func TestSession_StarLostFailsAfterTheBudget(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.MaxLostFrames = 3
	s := startedSession(t, cfg)

	_, err := s.Update(Observation{TSec: 1, Found: false})
	require.NoError(t, err)
	_, err = s.Update(Observation{TSec: 2, Found: false})
	require.NoError(t, err)

	sample, err := s.Update(Observation{TSec: 3, Found: false})
	require.ErrorIs(t, err, ErrStarLost)
	assert.Equal(t, PhaseFailed, s.Phase())
	assert.False(t, sample.Valid, "the failing sample is still returned so the gap is visible")
}

func TestSession_RunawayErrorAborts(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.RunawayArcsec = 30
	cfg.RunawaySamples = 3
	s := startedSession(t, cfg)

	// 20 px at 2″/px is 40″ — far beyond anything guiding produces, since the reference started at the
	// star.
	_, err := s.Update(Observation{TSec: 1, Found: true, X: 120, Y: 100})
	require.NoError(t, err, "one excursion could be a cosmic ray on the centroid")
	_, err = s.Update(Observation{TSec: 2, Found: true, X: 120, Y: 100})
	require.NoError(t, err)

	_, err = s.Update(Observation{TSec: 3, Found: true, X: 120, Y: 100})
	require.ErrorIs(t, err, ErrDiverging)
	assert.Equal(t, PhaseFailed, s.Phase())
	assert.Contains(t, err.Error(), "calibration sign", "the message should name the usual cause")
}

func TestSession_RunawayRunResetsWhenTheStarComesBack(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.RunawayArcsec = 30
	cfg.RunawaySamples = 3
	s := startedSession(t, cfg)

	_, _ = s.Update(Observation{TSec: 1, Found: true, X: 120, Y: 100})
	_, _ = s.Update(Observation{TSec: 2, Found: true, X: 120, Y: 100})
	_, err := s.Update(Observation{TSec: 3, Found: true, X: 100, Y: 100})
	require.NoError(t, err)

	// Two more big ones must not tip it over: the run restarted.
	_, err = s.Update(Observation{TSec: 4, Found: true, X: 120, Y: 100})
	require.NoError(t, err)
	_, err = s.Update(Observation{TSec: 5, Found: true, X: 120, Y: 100})
	require.NoError(t, err)
}

func TestSession_DitherMovesTheReferenceAndResettles(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.SettleArcsec = 1
	cfg.SettleSamples = 2
	s := startedSession(t, cfg)
	for i := 1; i <= 2; i++ {
		_, _ = s.Update(Observation{TSec: float64(i), Found: true, X: 100, Y: 100})
	}
	require.True(t, s.Settled())

	s.Dither(10, -5)

	x, y, _ := s.Reference()
	assert.Equal(t, 110.0, x)
	assert.Equal(t, 95.0, y)
	assert.Equal(t, PhaseDithering, s.Phase())
	assert.False(t, s.Settled(), "the sequencer must wait rather than expose immediately")

	// The star is still where it was, so there is now a real error to walk out — and the servo, not an
	// open-loop nudge, is what closes it.
	sample, err := s.Update(Observation{TSec: 3, Found: true, X: 100, Y: 100})
	require.NoError(t, err)
	assert.InDelta(t, -20, sample.RAErrArcsec, 1e-9)
	assert.InDelta(t, 20, sample.RACorrArcsec, 1e-9)

	for i := 4; i <= 6; i++ {
		_, err = s.Update(Observation{TSec: float64(i), Found: true, X: 110, Y: 95})
		require.NoError(t, err)
	}
	assert.True(t, s.Settled(), "once the star reaches the new reference, guiding resumes")
}

func TestSession_DitherBeforeAReferenceIsANoop(t *testing.T) {
	s, err := NewSession(plainConfig(ModeGuideScope), squareCal())
	require.NoError(t, err)

	s.Dither(10, 10)

	_, _, ok := s.Reference()
	assert.False(t, ok, "there is nothing to offset yet")
}

func TestSession_MetricsCountAppliedAndSuppressed(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.RA.MinMoveArcsec = 5
	cfg.Dec.MinMoveArcsec = 5
	s := startedSession(t, cfg)

	// 1 px is 2″ on each axis: inside the deadband, so both axes withhold.
	_, err := s.Update(Observation{TSec: 1, Found: true, X: 101, Y: 101})
	require.NoError(t, err)
	// 5 px is 10″: outside it, so both axes act.
	_, err = s.Update(Observation{TSec: 2, Found: true, X: 105, Y: 105})
	require.NoError(t, err)

	m := s.Metrics()
	assert.Equal(t, 2, s.Metrics().Suppressed, "one sample × two quiet axes")
	assert.Equal(t, 2, m.Corrections, "one sample × two acting axes")
	assert.Positive(t, m.RMSTotalArcsec)
	assert.Positive(t, m.RMSTotalPx)
	assert.InDelta(t, 10*1.4142, m.PeakArcsec, 0.01)
}

func TestSession_MetricsCountClampedCorrections(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.RA.MaxMoveArcsec = 2
	cfg.Dec.MaxMoveArcsec = 2
	s := startedSession(t, cfg)

	_, err := s.Update(Observation{TSec: 1, Found: true, X: 120, Y: 120})
	require.NoError(t, err)

	m := s.Metrics()
	assert.Equal(t, 2, m.Clamped)
	assert.Equal(t, 2, m.Corrections, "a clamped correction is still a correction")
}

func TestSession_AppliesTheDeclinationRescale(t *testing.T) {
	s := startedSession(t, plainConfig(ModeGuideScope))

	atEquator, err := s.Update(Observation{TSec: 1, Found: true, X: 105, Y: 100, HasDec: true, DecDeg: 0})
	require.NoError(t, err)
	atSixty, err := s.Update(Observation{TSec: 2, Found: true, X: 105, Y: 100, HasDec: true, DecDeg: 60})
	require.NoError(t, err)

	assert.InDelta(t, 2*atEquator.RAErrArcsec, atSixty.RAErrArcsec, 1e-9,
		"the same pixel offset is twice the axis rotation at 60° declination")
	assert.InDelta(t, atEquator.DecErrArcsec, atSixty.DecErrArcsec, 1e-9)
}

func TestSession_RetainsABoundedHistory(t *testing.T) {
	s := startedSession(t, plainConfig(ModeGuideScope))

	for i := 1; i <= maxRetainedSamples+500; i++ {
		_, err := s.Update(Observation{TSec: float64(i), Found: true, X: 100, Y: 100})
		require.NoError(t, err)
	}

	assert.Len(t, s.Samples(), maxRetainedSamples, "history is capped so a long night cannot grow without bound")
	assert.Greater(t, s.Metrics().Samples, maxRetainedSamples, "but the counts still reflect the whole run")
}

func TestSession_SetRetainedBytesSurfacesTheFrameBudget(t *testing.T) {
	s := startedSession(t, plainConfig(ModeGuideScope))

	s.SetRetainedBytes(256 * 1024)

	assert.Equal(t, int64(256*1024), s.Metrics().RetainedBytes,
		"the caller's frame budget must be visible in the UI, not just promised")
}

func TestSession_DivergenceGuardCatchesGrowthUnderCorrection(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.SettleArcsec = 100 // settle immediately so the growth guard arms
	cfg.SettleSamples = 1
	cfg.RunawayArcsec = 1e9 // isolate the growth test from the hard limit
	cfg.DivergenceWindow = 5
	cfg.DivergenceFactor = 4
	s := startedSession(t, cfg)

	// Establish a baseline of a steady 2″ error (1 px).
	for i := 1; i <= 6; i++ {
		_, err := s.Update(Observation{TSec: float64(i), Found: true, X: 101, Y: 100})
		require.NoError(t, err)
	}
	require.Equal(t, PhaseGuiding, s.Phase())

	// Now it grows tenfold and stays there — the loop is making things worse.
	var err error
	for i := 7; i <= 20 && err == nil; i++ {
		_, err = s.Update(Observation{TSec: float64(i), Found: true, X: 110, Y: 100})
	}
	require.ErrorIs(t, err, ErrDiverging)
	assert.Equal(t, PhaseFailed, s.Phase())
}

func TestSession_DivergenceGuardIgnoresANearZeroBaseline(t *testing.T) {
	cfg := plainConfig(ModeGuideScope)
	cfg.SettleArcsec = 100
	cfg.SettleSamples = 1
	cfg.DivergenceWindow = 5
	s := startedSession(t, cfg)

	// A perfectly guided run has a baseline of zero, and every later error is an infinite multiple of
	// it. Without this guard a flawless session would abort the moment seeing twitched.
	for i := 1; i <= 6; i++ {
		_, err := s.Update(Observation{TSec: float64(i), Found: true, X: 100, Y: 100})
		require.NoError(t, err)
	}
	for i := 7; i <= 20; i++ {
		_, err := s.Update(Observation{TSec: float64(i), Found: true, X: 100.3, Y: 100})
		require.NoError(t, err, "a tiny error over a zero baseline is not divergence")
	}
}
