package guide

import "math"

// Servo defaults. Zero means "use the default" throughout, following the convention the simulator's
// Config already uses, so a caller can set one field without silently disabling the rest.
const (
	defaultAggressiveness = 0.7
	// defaultRAHysteresis smooths the right-ascension channel a little. RA error is dominated by the
	// worm, which is smooth, so some carry-over from the previous correction rejects centroid noise
	// without adding lag worth worrying about.
	defaultRAHysteresis = 0.15
	// defaultDecResistSwitch is how many consecutive samples must agree before the declination axis is
	// allowed to reverse. Three is enough to outlast seeing but not a real trend.
	defaultDecResistSwitch = 3
	// defaultMinMoveArcsec is the deadband. Below it the servo does nothing, because the error is
	// indistinguishable from seeing and correcting it just injects the noise back into the mount.
	defaultMinMoveArcsec = 0.15
	// defaultMaxMoveArcsec caps one correction. One bad centroid — a cosmic ray, a satellite, a
	// neighbour mistaken for the guide star — must not be able to throw the mount off the target.
	defaultMaxMoveArcsec = 5.0
)

// Reasons a correction was reduced or withheld. They are values rather than free text so the UI can
// count them and a test can assert on them.
const (
	WhyApplied  = ""
	WhyDeadband = "below min move"
	WhyResist   = "resisting direction switch"
	WhyClamped  = "clamped to max move"
	WhyInvalid  = "error not finite"
)

// AxisConfig tunes one axis of the servo.
//
// The two axes want genuinely different policies, which is why this is per-axis rather than global.
// Right ascension is driven continuously against a smooth worm error and benefits from hysteresis;
// declination sits still until it has to reverse through backlash, and benefits from refusing to
// reverse on a single sample.
type AxisConfig struct {
	// Aggressiveness is the fraction of the measured error corrected each sample. Below 1 on purpose:
	// correcting the whole error every time turns any calibration scale error into an oscillation.
	Aggressiveness float64 `json:"aggressiveness"`
	// Hysteresis is how much of the previous correction carries into this one, 0–1.
	Hysteresis float64 `json:"hysteresis"`
	// MinMoveArcsec is the deadband below which nothing is commanded.
	MinMoveArcsec float64 `json:"min_move_arcsec"`
	// MaxMoveArcsec clamps a single correction.
	MaxMoveArcsec float64 `json:"max_move_arcsec"`
	// ResistSwitch is how many consecutive samples must call for the opposite direction before it is
	// obeyed. 0 disables the guard.
	ResistSwitch int `json:"resist_switch"`
}

// DefaultAxisConfig returns the tuning for one axis.
func DefaultAxisConfig(axis AxisID) AxisConfig {
	c := AxisConfig{
		Aggressiveness: defaultAggressiveness,
		MinMoveArcsec:  defaultMinMoveArcsec,
		MaxMoveArcsec:  defaultMaxMoveArcsec,
	}
	if axis == AxisDec {
		c.ResistSwitch = defaultDecResistSwitch
	} else {
		c.Hysteresis = defaultRAHysteresis
	}
	return c
}

func (c AxisConfig) withDefaults() AxisConfig {
	if c.Aggressiveness <= 0 {
		c.Aggressiveness = defaultAggressiveness
	}
	if c.MinMoveArcsec <= 0 {
		c.MinMoveArcsec = defaultMinMoveArcsec
	}
	if c.MaxMoveArcsec <= 0 {
		c.MaxMoveArcsec = defaultMaxMoveArcsec
	}
	if c.Hysteresis < 0 {
		c.Hysteresis = 0
	}
	if c.Hysteresis > 1 {
		c.Hysteresis = 1
	}
	return c
}

// Axis is one axis of the servo. It is stateful — hysteresis and the direction guard both remember
// the previous sample — so there is one per axis per session, and it is not safe for concurrent use.
type Axis struct {
	cfg AxisConfig

	prevOut     float64
	lastDir     int
	switchCount int
}

// NewAxis builds a servo axis. A zero AxisConfig is filled in with the defaults for that axis.
func NewAxis(axis AxisID, cfg AxisConfig) *Axis {
	if cfg == (AxisConfig{}) {
		cfg = DefaultAxisConfig(axis)
	}
	return &Axis{cfg: cfg.withDefaults()}
}

// Config returns the tuning in force, after defaults.
func (a *Axis) Config() AxisConfig { return a.cfg }

// Reset forgets the previous sample. Call it after anything that legitimately moves the star — a
// slew, a dither, a re-acquired reference — so the carried-over correction and the direction guard do
// not fight the new position.
func (a *Axis) Reset() {
	a.prevOut = 0
	a.lastDir = 0
	a.switchCount = 0
}

// Next turns a measured axis error into the correction to command.
//
// errArcsec is where the star IS minus where it SHOULD be, in axis arcseconds; the returned
// correction is the axis rotation that removes it, and therefore has the opposite sign. The second
// result explains a correction that was withheld or reduced — empty when it was applied as computed.
func (a *Axis) Next(errArcsec float64) (float64, string) {
	if math.IsNaN(errArcsec) || math.IsInf(errArcsec, 0) {
		return 0, WhyInvalid
	}
	if math.Abs(errArcsec) < a.cfg.MinMoveArcsec {
		// Deliberately does not touch the servo state. The deadband means "no information worth
		// acting on", not "the star is centred", and resetting the direction guard here would let a
		// reversal slip through on the next sample without the consecutive agreement it requires.
		return 0, WhyDeadband
	}

	want := -errArcsec * a.cfg.Aggressiveness
	if a.cfg.Hysteresis > 0 {
		want = (1-a.cfg.Hysteresis)*want + a.cfg.Hysteresis*a.prevOut
	}

	dir := sign(want)
	if a.cfg.ResistSwitch > 0 && a.lastDir != 0 && dir != 0 && dir != a.lastDir {
		a.switchCount++
		if a.switchCount < a.cfg.ResistSwitch {
			// Backlash means a declination reversal costs real time to take up. Reversing on a single
			// noisy sample spends that time and then reverses back, which is how a guider ends up
			// oscillating across the backlash band instead of sitting in it.
			return 0, WhyResist
		}
	}
	a.switchCount = 0

	why := WhyApplied
	if math.Abs(want) > a.cfg.MaxMoveArcsec {
		want = math.Copysign(a.cfg.MaxMoveArcsec, want)
		why = WhyClamped
	}

	a.prevOut = want
	if d := sign(want); d != 0 {
		a.lastDir = d
	}
	return want, why
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
