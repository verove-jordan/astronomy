// Package guide turns a measured star offset into a mount correction.
//
// It is the correction half that internal/tracking deliberately left out. That package measures how
// the mount really tracked and stops there, on the stated grounds that "it moves hardware on a model
// fitted from data, needs on-sky iteration to trust, and the honest order is to measure first". The
// measuring is done, so this is that half — with the caution kept: every policy here exists to stop a
// bad measurement reaching the motors, and each one names the failure it prevents.
//
// # Two modes, one servo
//
// Two quite different arrangements produce an error signal, and they share everything downstream:
//
//   - ModeSelfGuide uses the imaging camera's own subs. One measurement per sub (30–300 s), so it can
//     only correct what is predictable between frames: polar-alignment drift and worm periodic error.
//     It cannot touch seeing or wind, and nothing at that cadence could.
//   - ModeGuideScope uses a second camera on a guide scope at roughly 1 Hz, closing a real feedback
//     loop that also catches the slower irregular errors.
//
// Both feed the same Calibration, the same Axis servo, the same clamps and the same divergence guard,
// so the policy is written and tested once. What differs is only where a Sample comes from and how
// often.
//
// # Sign convention, stated once
//
// Everything here is in AXIS arcseconds, never sky arcseconds. The distinction is the one that bites:
// a given axis rotation moves the sky by cos(dec) times as much, so a correction computed in sky
// terms and written to a motor is wrong by that factor — and wrong in a way that still looks
// plausible at low declination. Calibration absorbs cos(dec) by construction (it was measured against
// the sky), exactly as the existing PECCalibration does, and Calibration.AtDec re-scales when the
// mount has moved to a different declination since.
//
// An "error" is where the star IS minus where it SHOULD be. A "correction" is the axis rotation to
// command, and is therefore the opposite sign. Getting this backwards makes the mount add the error
// instead of removing it, which is why Session carries a divergence guard rather than trusting the
// arithmetic.
package guide

import (
	"errors"
	"math"
)

// Errors callers branch on.
var (
	// ErrNotCalibrated means a correction was asked for before calibration succeeded. Guiding on an
	// unknown pixel→axis mapping is worse than not guiding: the direction is a guess.
	ErrNotCalibrated = errors.New("guider not calibrated")
	// ErrCalibrationDegenerate means the two axes came out too nearly parallel to be separated. It is
	// what a mirrored or heavily rotated optical train looks like, and also what a calibration that
	// never actually moved the mount looks like.
	ErrCalibrationDegenerate = errors.New("calibration axes are degenerate")
	// ErrCalibrationTooSmall means the star did not move far enough for the fit to mean anything.
	ErrCalibrationTooSmall = errors.New("calibration movement too small")
	// ErrDiverging means the error is growing under correction. The usual cause is an inverted sign,
	// and the only safe response is to stop: a guider that keeps pushing walks the mount off target.
	ErrDiverging = errors.New("guiding is diverging")
	// ErrStarLost means too many consecutive frames had no usable star. Cloud, dew, a passing
	// aeroplane. Ordinary, but past some run of frames the reference is no longer trustworthy.
	ErrStarLost = errors.New("guide star lost")
)

// Mode selects where the error signal comes from.
type Mode string

const (
	// ModeOff disables guiding without removing the configuration.
	ModeOff Mode = "off"
	// ModeSelfGuide predicts from the imaging camera's own subs. No extra hardware.
	ModeSelfGuide Mode = "selfguide"
	// ModeGuideScope closes a fast loop on a dedicated guide camera.
	ModeGuideScope Mode = "guidescope"
)

// Valid reports whether m is a mode this package implements. Anything else is a typo in config, and
// silently treating it as "off" would hide a night of not guiding.
func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeSelfGuide, ModeGuideScope:
		return true
	}
	return false
}

// Guiding reports whether the mode actually drives the mount.
func (m Mode) Guiding() bool { return m == ModeSelfGuide || m == ModeGuideScope }

// AxisID names a mount axis. The values match the sense of Mount.Nudge's arguments: RA first, then
// declination.
type AxisID int

const (
	AxisRA AxisID = iota
	AxisDec
)

func (a AxisID) String() string {
	if a == AxisDec {
		return "dec"
	}
	return "ra"
}

// Phase is the guider's state. It is reported rather than inferred so the UI can say "settling" and
// mean it, instead of showing a large error as though it were a failure.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseCalibrating Phase = "calibrating"
	// PhaseSettling follows a slew or a dither: the error is expected to be large and shrinking, so
	// the divergence guard is not armed yet.
	PhaseSettling Phase = "settling"
	PhaseGuiding  Phase = "guiding"
	// PhaseStarLost keeps the clock running and keeps looking where the star was. It never moves the
	// mount to re-acquire — the same policy the periodic-error training run uses, and for the same
	// reason: a hunt turns a lost star into a lost reference.
	PhaseStarLost  Phase = "star_lost"
	PhaseDithering Phase = "dithering"
	PhaseFailed    Phase = "failed"
)

// Sample is one measurement and what was done about it. It is the unit the UI graphs, the store
// persists and internal/tracking analyses, so it carries both the error and the response.
type Sample struct {
	// TSec is seconds since the session started.
	TSec float64 `json:"t_sec"`
	// Valid is false when no star was measured. The sample is still recorded: a gap in the series is
	// information, and dropping it would make a cloud look like good guiding.
	Valid bool `json:"valid"`

	// DX and DY are the star's offset from the reference position, in pixels.
	DX float64 `json:"dx_px"`
	DY float64 `json:"dy_px"`

	// Errors and corrections are AXIS arcseconds. See the package comment.
	RAErrArcsec   float64 `json:"ra_err_arcsec"`
	DecErrArcsec  float64 `json:"dec_err_arcsec"`
	RACorrArcsec  float64 `json:"ra_corr_arcsec"`
	DecCorrArcsec float64 `json:"dec_corr_arcsec"`

	// RAWhy and DecWhy explain a zero correction. A guider that silently does nothing is
	// indistinguishable from one that is broken, and these are what tell them apart.
	RAWhy  string `json:"ra_why,omitempty"`
	DecWhy string `json:"dec_why,omitempty"`

	SNR float64 `json:"snr,omitempty"`
	HFD float64 `json:"hfd,omitempty"`
}

// TotalErrArcsec is the combined error magnitude of one sample.
func (s Sample) TotalErrArcsec() float64 {
	return math.Hypot(s.RAErrArcsec, s.DecErrArcsec)
}
