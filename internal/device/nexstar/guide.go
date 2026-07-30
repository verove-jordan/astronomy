package nexstar

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The mount implements the optional guiding capability, asserted here so a signature change is a
// compile error rather than a type assertion that quietly starts failing at run time.
var _ device.GuideMount = (*Mount)(nil)

// minPulseDuration is the shortest pulse worth commanding.
//
// The link runs at 9600 baud, so the eight-byte frame that starts the axis takes about 8 ms on the
// wire and the one that stops it takes as long again. Asking for a 20 ms pulse therefore delivers
// something between 20 and 40 ms, and the error is systematic rather than noise — it always
// over-moves. Rather than return an inaccurate move, short requests are re-expressed as a slower
// pulse of this length, which covers the same angle with the timing error pushed down to a few
// percent.
const minPulseDuration = 100 * time.Millisecond

// maxPulseDuration bounds a single pulse. A guide correction is a fraction of a second; anything this
// long is a bug in the caller's arithmetic, and the failure mode of an unbounded pulse is a mount
// driving into the tripod.
const maxPulseDuration = 10 * time.Second

// PulseGuide turns one axis at a commanded rate for a commanded time, then stops it.
//
// The rate is AXIS arcseconds per second — motor rotation, not sky motion. See device.GuideMount for
// why that distinction matters.
func (m *Mount) PulseGuide(ctx context.Context, axis device.GuideAxis, arcsecPerSec float64, d time.Duration) error {
	if d <= 0 || arcsecPerSec == 0 {
		return nil
	}
	if d > maxPulseDuration {
		return fmt.Errorf("guide pulse of %s exceeds the %s ceiling", d, maxPulseDuration)
	}
	if math.IsNaN(arcsecPerSec) || math.IsInf(arcsecPerSec, 0) {
		return fmt.Errorf("guide rate %v is not a finite number", arcsecPerSec)
	}

	// Preserve the angle rather than the rate when the request is too short to time accurately.
	if d < minPulseDuration {
		arcsecPerSec = arcsecPerSec * d.Seconds() / minPulseDuration.Seconds()
		d = minPulseDuration
	}

	motor := axisAzmRA
	if axis == device.GuideAxisDec {
		motor = axisAltDec
	}

	m.mu.Lock()
	if m.port == nil {
		m.mu.Unlock()
		return device.ErrNotConnected
	}
	_, err := m.rawLocked(slewRateCommand(motor, arcsecPerSec))
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start %s guide pulse: %w", axis, err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(d):
	}

	// Stopping is not conditional on anything, including a cancelled context. The same rule the dither
	// nudge follows: a pulse that never stops is a mount that walks away, and a cancelled guide session
	// must leave the mount tracking, not slewing.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return nil
	}
	if _, err := m.rawLocked(slewRateCommand(motor, 0)); err != nil {
		return fmt.Errorf("stop %s guide pulse: %w", axis, err)
	}
	return nil
}

// GuideRate reads the right-ascension motor's configured autoguide rate, as a fraction of sidereal.
//
// One axis is read rather than both because the two are set together by SetGuideRate and by every
// hand controller, and a caller that only wants to size its pulses gains nothing from two round trips
// on a 9600-baud link.
func (m *Mount) GuideRate(ctx context.Context) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return 0, device.ErrNotConnected
	}
	// Read by LENGTH, never by delimiter. The reply is a raw byte, and the perfectly ordinary rate
	// value 35 IS '#' — scanning for the terminator would hand back an empty body and leave the real
	// '#' in the buffer, after which every later command reads the previous one's reply.
	reply, err := m.rawBinaryLocked(getGuideRateCommand(axisAzmRA), 1)
	if err != nil {
		return 0, fmt.Errorf("read autoguide rate: %w", err)
	}
	if len(reply) < 1 {
		return 0, fmt.Errorf("empty autoguide rate reply")
	}
	return float64(reply[0]) / autoguideRateScale, nil
}

// SetGuideRate configures the autoguide rate on both motors.
func (m *Mount) SetGuideRate(ctx context.Context, fraction float64) error {
	if math.IsNaN(fraction) {
		return fmt.Errorf("guide rate is not a number")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	for _, motor := range []int{axisAzmRA, axisAltDec} {
		if _, err := m.rawLocked(setGuideRateCommand(motor, fraction)); err != nil {
			return fmt.Errorf("set autoguide rate on motor %d: %w", motor, err)
		}
	}
	return nil
}
