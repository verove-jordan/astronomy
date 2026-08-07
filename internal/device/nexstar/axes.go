package nexstar

import (
	"context"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The mount's shaft angles, read straight from the motor controllers.
//
// This is the only position an unaligned mount can state truthfully. `e`/`E` answer from the hand
// controller's pointing model, so before an alignment they are not a measurement of anything — ours
// cheerfully reported altitude 90° with the tube nowhere near the zenith. The motor controllers have
// no model to be wrong about: they report where their shafts are, and they do it whether or not the
// hand controller has ever been aligned.
//
// Everything the app does without the hand controller stands on that distinction. A pointing model
// fitted here is the app's own — the mount contributes measurements, not opinions.

// AxisAngles is one reading of both shafts, in degrees of shaft rotation. They are NOT sky
// coordinates: converting between the two is exactly what a pointing model does, and the offsets it
// needs are unknown until the first star has been centred.
type AxisAngles struct {
	// RADeg is the RA/azimuth shaft — the one the worm and PEC belong to.
	RADeg float64 `json:"ra_deg"`
	// DecDeg is the declination/altitude shaft.
	DecDeg float64 `json:"dec_deg"`
}

// AxisAngles reads both shafts. The two reads are deliberately sequential: they share one serial
// port under one mutex, so there is no concurrency to win, and a mount that answers the first but
// not the second should report that rather than a half-filled struct.
func (m *Mount) AxisAngles(_ context.Context) (AxisAngles, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return AxisAngles{}, device.ErrNotConnected
	}
	ra, err := m.axisAngleLocked(axisAzmRA)
	if err != nil {
		return AxisAngles{}, fmt.Errorf("read RA shaft: %w", err)
	}
	dec, err := m.axisAngleLocked(axisAltDec)
	if err != nil {
		return AxisAngles{}, fmt.Errorf("read declination shaft: %w", err)
	}
	return AxisAngles{RADeg: ra, DecDeg: dec}, nil
}

func (m *Mount) axisAngleLocked(axis int) (float64, error) {
	reply, err := m.rawBinaryLocked(passthrough(byte(axis), mcGetPosition, nil, 3), 3)
	if err != nil {
		return 0, err
	}
	return decodeAxisPosition(reply)
}
