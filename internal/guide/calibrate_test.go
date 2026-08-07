package guide

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMount is a Stepper over a known true mapping, so a calibration run can be checked against the
// answer it was supposed to find.
type fakeMount struct {
	cal Calibration
	// axisArcsec is how far each axis has been driven from its start.
	axisArcsec map[AxisID]float64
	// decBacklash is the slack a declination reversal has to wind out before the load follows.
	decBacklash float64
	decDir      int
	decTakeUp   float64

	// loseFor makes the next n measurements report no star.
	loseFor int
	failAt  int
	calls   int
}

func newFakeMount(cal Calibration) *fakeMount {
	return &fakeMount{cal: cal, axisArcsec: map[AxisID]float64{}}
}

func (f *fakeMount) step(_ context.Context, axis AxisID, arcsec float64) (float64, float64, bool, error) {
	f.calls++
	if f.failAt > 0 && f.calls >= f.failAt {
		return 0, 0, false, errors.New("serial link died")
	}
	if axis == AxisDec && arcsec != 0 && f.decBacklash > 0 {
		arcsec = f.applyBacklash(arcsec)
	}
	f.axisArcsec[axis] += arcsec

	if f.loseFor > 0 {
		f.loseFor--
		return 0, 0, false, nil
	}
	ra, dec := f.axisArcsec[AxisRA], f.axisArcsec[AxisDec]
	x := 500 + ra*f.cal.RAUnitX/f.cal.RAArcsecPerPx + dec*f.cal.DecUnitX/f.cal.DecArcsecPerPx
	y := 500 + ra*f.cal.RAUnitY/f.cal.RAArcsecPerPx + dec*f.cal.DecUnitY/f.cal.DecArcsecPerPx
	return x, y, true, nil
}

func (f *fakeMount) applyBacklash(arcsec float64) float64 {
	dir := 1
	if arcsec < 0 {
		dir = -1
	}
	if f.decDir != 0 && dir != f.decDir {
		f.decTakeUp = f.decBacklash
	}
	f.decDir = dir
	mag := math.Abs(arcsec)
	absorbed := math.Min(f.decTakeUp, mag)
	f.decTakeUp -= absorbed
	return float64(dir) * (mag - absorbed)
}

func TestCalibrate_RecoversTheTrueMapping(t *testing.T) {
	want := squareCal()
	f := newFakeMount(want)

	got, err := Calibrate(context.Background(), f.step, CalibrateOptions{DecDeg: 41})
	require.NoError(t, err)

	assert.InDelta(t, want.RAArcsecPerPx, got.RAArcsecPerPx, 1e-6)
	assert.InDelta(t, want.DecArcsecPerPx, got.DecArcsecPerPx, 1e-6)
	assert.InDelta(t, 1, got.RAUnitX, 1e-6)
	assert.InDelta(t, 1, got.DecUnitY, 1e-6)
	assert.InDelta(t, 41, got.DecAtCalibDeg, 1e-9)
	assert.True(t, got.Valid())
}

func TestCalibrate_RecoversARotatedMapping(t *testing.T) {
	const rot = 25 * math.Pi / 180
	want := Calibration{
		RAArcsecPerPx: 4, DecArcsecPerPx: 4,
		RAUnitX: math.Cos(rot), RAUnitY: math.Sin(rot),
		DecUnitX: -math.Sin(rot), DecUnitY: math.Cos(rot),
	}
	f := newFakeMount(want)

	got, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.NoError(t, err)

	assert.InDelta(t, 4, got.RAArcsecPerPx, 1e-6)
	assert.InDelta(t, want.RAUnitX, got.RAUnitX, 1e-6)
	assert.InDelta(t, want.RAUnitY, got.RAUnitY, 1e-6)
	assert.InDelta(t, 1, got.Orthogonality, 1e-6)
}

func TestCalibrate_ReturnsTheMountToWhereItStarted(t *testing.T) {
	f := newFakeMount(squareCal())

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.NoError(t, err)

	// Leaving the mount fifteen pixels out would hand the guider a large error as its first act, and on
	// a long focal length could carry the target off the sensor altogether.
	assert.InDelta(t, 0, f.axisArcsec[AxisRA], 1e-9, "the RA axis must end where it began")
	assert.InDelta(t, 0, f.axisArcsec[AxisDec], 1e-9)
}

func TestCalibrate_StopsOnceTheStarHasMovedFarEnough(t *testing.T) {
	// At 2″ per pixel and 20″ steps the star moves 10 px a step, so two steps clear a 15 px target and
	// a third would be waste — calibration should not drive the star any further than it needs to.
	f := newFakeMount(squareCal())

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{
		StepArcsec: 20, TargetDriftPx: 15,
	})
	require.NoError(t, err)

	// Per axis: 1 initial measurement + 2 outward + 2 return = 5, for two axes.
	assert.Equal(t, 10, f.calls)
}

func TestCalibrate_MeasuresDeclinationBacklash(t *testing.T) {
	f := newFakeMount(squareCal())
	f.decBacklash = 6

	got, err := Calibrate(context.Background(), f.step, CalibrateOptions{StepArcsec: 20, TargetDriftPx: 15})
	require.NoError(t, err)

	// The first reversed step delivers 20 − 6 = 14″ instead of 20″, so it falls 6″ short.
	assert.InDelta(t, 6, got.DecBacklashArcsec, 0.5,
		"the shortfall on the first reversal is exactly what the slack is")
}

func TestCalibrate_ReportsNoBacklashWhenThereIsNone(t *testing.T) {
	f := newFakeMount(squareCal())

	got, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.NoError(t, err)

	assert.InDelta(t, 0, got.DecBacklashArcsec, 1e-6)
}

func TestCalibrate_ToleratesABrieflyLostStar(t *testing.T) {
	f := newFakeMount(squareCal())
	f.loseFor = 1

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	assert.NoError(t, err, "one cloudy frame should not waste the whole calibration")
}

func TestCalibrate_GivesUpOnAPersistentlyLostStar(t *testing.T) {
	f := newFakeMount(squareCal())
	f.loseFor = 99

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.ErrorIs(t, err, ErrStarLost)
}

func TestCalibrate_ARepeatedMeasurementDoesNotMoveTheMountAgain(t *testing.T) {
	// The pulse already happened when the star went missing. Re-issuing it to get a reading would
	// corrupt the commanded total the fit is regressed against, and the scale would come out wrong.
	f := newFakeMount(squareCal())
	f.loseFor = 2

	got, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.NoError(t, err)
	assert.InDelta(t, 2, got.RAArcsecPerPx, 1e-6, "the scale must survive a re-measurement")
}

func TestCalibrate_FailsWhenTheMountDoesNotMove(t *testing.T) {
	// A stuck axis, a disconnected motor, tracking left off — all look like this, and all must be
	// refused rather than fitted into a mapping that would then steer the telescope.
	f := newFakeMount(Calibration{
		RAArcsecPerPx: 1e6, DecArcsecPerPx: 1e6,
		RAUnitX: 1, DecUnitY: 1,
	})

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{MaxSteps: 4})
	require.ErrorIs(t, err, ErrCalibrationTooSmall)
}

func TestCalibrate_PropagatesAStepperFailure(t *testing.T) {
	f := newFakeMount(squareCal())
	f.failAt = 3

	_, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "serial link died")
}

func TestCalibrate_ResultDrivesTheServoCorrectly(t *testing.T) {
	// The end-to-end point of calibrating at all: the fitted mapping, fed back through Axes, must turn a
	// pixel offset into the axis error that produced it.
	trueMapping := squareCal()
	trueMapping.RAUnitX, trueMapping.RAUnitY = 0.8, 0.6
	trueMapping.DecUnitX, trueMapping.DecUnitY = -0.6, 0.8
	f := newFakeMount(trueMapping)

	cal, err := Calibrate(context.Background(), f.step, CalibrateOptions{})
	require.NoError(t, err)

	// Drive the axes to a known place and check the servo reads it back.
	const wantRA, wantDec = 12.0, -7.0
	dx := wantRA*trueMapping.RAUnitX/trueMapping.RAArcsecPerPx + wantDec*trueMapping.DecUnitX/trueMapping.DecArcsecPerPx
	dy := wantRA*trueMapping.RAUnitY/trueMapping.RAArcsecPerPx + wantDec*trueMapping.DecUnitY/trueMapping.DecArcsecPerPx

	gotRA, gotDec, ok := cal.Axes(dx, dy)
	require.True(t, ok)
	assert.InDelta(t, wantRA, gotRA, 1e-6)
	assert.InDelta(t, wantDec, gotDec, 1e-6)
}
