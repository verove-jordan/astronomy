package guide

import (
	"context"
	"fmt"
	"math"
)

// Calibration-run defaults.
const (
	// defaultStepArcsec is one calibration pulse. Large enough to outrun seeing and the centroid noise,
	// small enough that a dozen of them do not carry the star off the sensor.
	defaultStepArcsec = 20.0
	// defaultTargetDriftPx is how far the star should travel before the fit is considered well
	// conditioned. Comfortably above the minCalibDriftPx floor, so an ordinary run clears it.
	defaultTargetDriftPx = 15.0
	// defaultMaxSteps bounds the run. A mount that has not moved the star after this many pulses is not
	// going to, and the honest answer is to fail rather than keep driving it.
	defaultMaxSteps = 12
	// maxLostDuringCalibration is how many starless measurements are tolerated. Calibration is short and
	// deliberate; if the star is not there, the run should be repeated rather than fitted around gaps.
	maxLostDuringCalibration = 3
)

// Stepper issues one calibration pulse and reports where the star ended up afterwards.
//
// A zero arcsec means "do not move, just measure" — which is how a run establishes its starting point
// without a separate callback. found is false when no star was measurable, which is an ordinary outcome
// the run counts rather than an error.
type Stepper func(ctx context.Context, axis AxisID, arcsec float64) (x, y float64, found bool, err error)

// CalibrateOptions tunes a calibration run.
type CalibrateOptions struct {
	StepArcsec    float64
	TargetDriftPx float64
	MaxSteps      int
	// DecDeg is where the mount is pointed, recorded in the result so the RA scale can be re-based when
	// the mount later moves to a different declination.
	DecDeg float64
}

func (o CalibrateOptions) withDefaults() CalibrateOptions {
	if o.StepArcsec <= 0 {
		o.StepArcsec = defaultStepArcsec
	}
	if o.TargetDriftPx <= 0 {
		o.TargetDriftPx = defaultTargetDriftPx
	}
	if o.MaxSteps <= 0 {
		o.MaxSteps = defaultMaxSteps
	}
	return o
}

// Calibrate measures the pixel↔axis mapping by moving each axis a known amount and watching the star.
//
// Both axes are driven out and then brought back, and the return leg matters as much as the outward
// one: a calibration that left the mount fifteen pixels from where it started would hand the guider a
// large error to correct as its first act, and on a long focal length it could carry the target off the
// sensor entirely.
//
// The fit uses only the outward leg. The return leg exists to restore the pointing and — on
// declination — to measure the backlash, which is visible as the shortfall on the first reversed step.
func Calibrate(ctx context.Context, step Stepper, opts CalibrateOptions) (Calibration, error) {
	opts = opts.withDefaults()

	var obs []CalibObservation
	backlash := make(map[AxisID]float64, 2)
	for _, axis := range []AxisID{AxisRA, AxisDec} {
		axisObs, shortfallPx, err := calibrateAxis(ctx, step, axis, opts)
		if err != nil {
			return Calibration{}, err
		}
		obs = append(obs, axisObs...)
		backlash[axis] = shortfallPx
	}

	cal, err := FitCalibration(obs, opts.DecDeg)
	if err != nil {
		return Calibration{}, err
	}
	// The shortfall was measured in pixels; the scale that turns it into an angle is only known now.
	cal.DecBacklashArcsec = backlash[AxisDec] * cal.DecArcsecPerPx
	return cal, nil
}

// calibrateAxis drives one axis out and back, returning the outward observations and how many pixels
// the first reversed step fell short of what the outward steps achieved.
func calibrateAxis(ctx context.Context, step Stepper, axis AxisID, opts CalibrateOptions) ([]CalibObservation, float64, error) {
	startX, startY, err := measure(ctx, step, axis, 0, opts)
	if err != nil {
		return nil, 0, err
	}

	var obs []CalibObservation
	var cumulative float64
	for i := 0; i < opts.MaxSteps; i++ {
		cumulative += opts.StepArcsec
		x, y, err := measure(ctx, step, axis, opts.StepArcsec, opts)
		if err != nil {
			return nil, 0, err
		}
		dx, dy := x-startX, y-startY
		obs = append(obs, CalibObservation{Axis: axis, CommandArcsec: cumulative, DX: dx, DY: dy})
		if math.Hypot(dx, dy) >= opts.TargetDriftPx {
			break
		}
	}
	if len(obs) < 2 {
		return nil, 0, fmt.Errorf("%w: %s axis produced %d usable steps",
			ErrCalibrationTooSmall, axis, len(obs))
	}
	last := obs[len(obs)-1]
	perStepPx := math.Hypot(last.DX, last.DY) / float64(len(obs))

	// Bring the axis back to where it started, one step at a time. The first reversed step is measured
	// on its own because that is the only one that pays for the backlash: how far it falls short of an
	// ordinary step is how much slack the gear had.
	var shortfallPx float64
	outerX, outerY := startX+last.DX, startY+last.DY
	for i := 0; i < len(obs); i++ {
		x, y, err := measure(ctx, step, axis, -opts.StepArcsec, opts)
		if err != nil {
			// The mount is part-way back. Report it rather than fitting: a calibration that cannot
			// restore the pointing has left the telescope somewhere the caller did not ask for.
			return nil, 0, fmt.Errorf("returning %s axis to its start: %w", axis, err)
		}
		if i == 0 {
			moved := math.Hypot(x-outerX, y-outerY)
			if s := perStepPx - moved; s > 0 {
				shortfallPx = s
			}
		}
	}
	return obs, shortfallPx, nil
}

// measure issues one pulse and returns the star's position, retrying while the star is briefly absent.
func measure(ctx context.Context, step Stepper, axis AxisID, arcsec float64, opts CalibrateOptions) (float64, float64, error) {
	for lost := 0; ; {
		x, y, found, err := step(ctx, axis, arcsec)
		if err != nil {
			return 0, 0, err
		}
		if found {
			return x, y, nil
		}
		lost++
		if lost >= maxLostDuringCalibration {
			return 0, 0, fmt.Errorf("%w: lost the star %d times while calibrating the %s axis",
				ErrStarLost, lost, axis)
		}
		// Re-measure without moving again: the pulse already happened, and repeating it would corrupt
		// the commanded total the fit depends on.
		arcsec = 0
	}
}
