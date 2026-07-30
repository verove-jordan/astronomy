package sim

import (
	"context"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

// The simulated mount is a drop-in for the real one's guiding capability too, so a guide loop can be
// developed and tested with no hardware — and so a driver signature change breaks the build here
// rather than at run time on a real telescope.
var _ device.GuideMount = (*Mount)(nil)

// simGuideRate is the autoguide rate the simulated motor controller reports, as a fraction of
// sidereal. Half sidereal is what most mounts ship with.
const simGuideRate = 0.5

// PulseGuide turns one axis at a commanded rate for a commanded time.
//
// It applies the move immediately rather than blocking for d, which is the same choice Nudge makes and
// what makes the simulator usable with an injected clock: a closed-loop test covering two worm periods
// issues hundreds of pulses, and sleeping through each of them would cost minutes of wall clock to
// learn nothing. The consequence is that the sky does not drift DURING a simulated pulse, which
// isolates the servo's arithmetic from the cadence — the thing a unit test wants to pin down.
func (m *Mount) PulseGuide(_ context.Context, axis device.GuideAxis, arcsecPerSec float64, d time.Duration) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	if d <= 0 || arcsecPerSec == 0 ||
		math.IsNaN(arcsecPerSec) || math.IsInf(arcsecPerSec, 0) {
		return nil
	}
	axisArcsec := arcsecPerSec * d.Seconds()

	m.world.mu.Lock()
	defer m.world.mu.Unlock()

	var xi, eta float64
	if axis == device.GuideAxisDec {
		eta = m.world.applyDecBacklashLocked(axisArcsec)
	} else {
		// Axis rotation into sky displacement. A rotation of the RA axis by A arcseconds carries the
		// star A·cos(dec) across the sky, and this is where a guider that confuses the two is caught:
		// omitting the factor here would make the simulator agree with a wrong correction.
		xi = axisArcsec * math.Cos(m.world.decDeg*math.Pi/180)
	}

	ra, dec := astro.TangentSky(m.world.raDeg, m.world.decDeg, xi/3600, eta/3600)
	m.world.raDeg, m.world.decDeg = normRA(ra), clampDec(dec)
	return nil
}

// applyDecBacklashLocked returns how much of a commanded declination rotation actually reaches the
// load, and records the gear state for next time. Caller holds world.mu.
//
// Reversing direction first winds out the slack: the axis turns, the encoder is satisfied, and the
// telescope does not move. Only what is left over past the take-up produces a displacement.
func (w *World) applyDecBacklashLocked(arcsec float64) float64 {
	if w.cfg.DecBacklashArcsec <= 0 || arcsec == 0 {
		return arcsec
	}
	dir := 1
	if arcsec < 0 {
		dir = -1
	}
	if w.decGuideDir != 0 && dir != w.decGuideDir {
		// A reversal re-opens the whole gap.
		w.decTakeUp = w.cfg.DecBacklashArcsec
	}
	w.decGuideDir = dir

	magnitude := math.Abs(arcsec)
	if w.decTakeUp > 0 {
		absorbed := math.Min(w.decTakeUp, magnitude)
		w.decTakeUp -= absorbed
		magnitude -= absorbed
	}
	return float64(dir) * magnitude
}

// GuideRate reports the simulated motor controller's configured autoguide rate.
func (m *Mount) GuideRate(context.Context) (float64, error) {
	if !m.connected {
		return 0, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	return m.world.guideRate, nil
}

// SetGuideRate configures it, clamped the way a real driver clamps rather than rejecting — the useful
// range is a property of the hardware, not of the caller.
func (m *Mount) SetGuideRate(_ context.Context, fraction float64) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	if math.IsNaN(fraction) {
		return nil
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	// Quantised to the same 1/256ths the wire protocol uses, so a test cannot pass here and then be
	// surprised by rounding on real hardware.
	m.world.guideRate = math.Round(fraction*256) / 256
	return nil
}
