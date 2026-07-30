package sim

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

// pointingErrorArcsec is how far the simulated mount misses a commanded GoTo. This is the whole
// reason plate-solve centring exists, so the simulator must not be perfect — it lands ~1 arcminute
// off, deterministically per target, and Sync corrects it.
const pointingErrorArcsec = 65

// Mount is a simulated GEM. It models the things that actually bite: slews take time, GoTo lands
// slightly off, tracking can be switched off, and nudges move by a known amount so dither feedback
// can be verified.
type Mount struct {
	world     *World
	connected bool
	syncOff   [2]float64 // accumulated sync correction (ξ, η degrees)
	// pecSeekDone is when the simulated index hunt finishes; zero when not seeking. Guarded by
	// world.mu, like the rest of the PEC state it reports on.
	pecSeekDone time.Time
}

// NewMount builds a simulated mount bound to a world.
func NewMount(w *World) *Mount { return &Mount{world: w} }

func (m *Mount) Connect(context.Context) error {
	m.connected = true
	return nil
}

func (m *Mount) Close() error {
	m.connected = false
	return nil
}

func (m *Mount) Connected() bool { return m.connected }

func (m *Mount) State(context.Context) (device.MountState, error) {
	if !m.connected {
		return device.MountState{}, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	now := m.world.now()
	m.settleLocked(now)
	ra, dec := m.world.pointingAt(now)
	alt, az := astro.Horizontal(ra, dec, m.world.cfg.LatDeg, m.world.cfg.LonDeg, now)
	side := "west"
	if astro.HourAngleDeg(ra, m.world.cfg.LonDeg, now) <= 0 {
		side = "east"
	}
	return device.MountState{
		Info: device.Info{ID: "sim-mount", Name: "Simulated Celestron AVX",
			Driver: "sim", Kind: device.KindMount},
		RADeg: ra, DecDeg: dec, AltDeg: alt, AzDeg: az,
		Slewing:  now.Before(m.world.slewUntil),
		Tracking: m.world.tracking, TrackingRate: m.world.trackingRate,
		Aligned: m.world.aligned, PierSide: side,
		Firmware: "sim-5.30", Model: "AVX (simulated)",
	}, nil
}

// settleLocked completes a slew whose time has come. Caller holds world.mu.
func (m *Mount) settleLocked(now time.Time) {
	if m.world.slewUntil.IsZero() || now.Before(m.world.slewUntil) {
		return
	}
	m.world.slewUntil = time.Time{}
	m.world.raDeg = m.world.targetRA
	m.world.decDeg = m.world.targetDec
}

func (m *Mount) GotoRADec(_ context.Context, raDeg, decDeg float64) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	if decDeg < -90 || decDeg > 90 {
		return fmt.Errorf("dec %.4f out of range", decDeg)
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	if !m.world.aligned {
		return fmt.Errorf("mount is not aligned — GoTo would point somewhere arbitrary")
	}
	// Land slightly off, the way a real mount does, minus whatever a previous Sync corrected.
	errXi, errEta := pointingErrorFor(raDeg, decDeg)
	targetRA, targetDec := astro.TangentSky(raDeg, decDeg,
		errXi-m.syncOff[0], errEta-m.syncOff[1])
	sep := astro.AngularSeparation(m.world.raDeg, m.world.decDeg, raDeg, decDeg)
	m.world.targetRA, m.world.targetDec = normRA(targetRA), clampDec(targetDec)
	m.world.cmdRA, m.world.cmdDec, m.world.hasCmd = raDeg, decDeg, true
	m.world.slewUntil = m.world.now().Add(
		time.Duration(math.Max(sep/m.world.cfg.SlewRateDegPerSec, 0.2) * float64(time.Second)))
	return nil
}

// pointingErrorFor is a deterministic per-target miss: same target, same error, so a test can assert
// that centring converges rather than chasing noise.
func pointingErrorFor(raDeg, decDeg float64) (dXi, dEta float64) {
	h := math.Sin(raDeg*12.9898+decDeg*78.233) * 43758.5453
	frac := h - math.Floor(h)
	angle := frac * 2 * math.Pi
	r := float64(pointingErrorArcsec) / 3600
	return r * math.Cos(angle), r * math.Sin(angle)
}

func (m *Mount) Sync(_ context.Context, raDeg, decDeg float64) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	now := m.world.now()
	m.settleLocked(now)
	// The caller says "you are really pointing HERE" (from a plate solve). The correction worth
	// learning is how far that is from what was last commanded — feeding back the current pointing
	// against itself would learn nothing, which is exactly the bug a centring loop would hide.
	if m.world.hasCmd {
		xi, eta, ok := astro.TangentPlane(m.world.cmdRA, m.world.cmdDec, raDeg, decDeg)
		if !ok {
			return fmt.Errorf("sync target too far from the last commanded position")
		}
		m.syncOff[0] += xi
		m.syncOff[1] += eta
	}
	m.world.raDeg, m.world.decDeg = raDeg, decDeg
	return nil
}

func (m *Mount) Abort(context.Context) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	if !m.world.slewUntil.IsZero() {
		// Stop where we are: freeze the current pointing rather than jumping to the target.
		ra, dec := m.world.pointingAt(m.world.now())
		m.world.raDeg, m.world.decDeg = ra, dec
		m.world.slewUntil = time.Time{}
	}
	return nil
}

func (m *Mount) Jog(_ context.Context, dir device.Direction, rate int) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	if rate < 0 || rate > 9 {
		return fmt.Errorf("rate %d out of range 0..9", rate)
	}
	if rate == 0 {
		return nil
	}
	// One jog step moves by a rate-scaled amount; enough for the UI to show motion.
	step := math.Pow(2, float64(rate)) / 3600 // arcsec-ish at low rates, degrees at high ones
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	switch dir {
	case device.DirNorth:
		m.world.decDeg = clampDec(m.world.decDeg + step)
	case device.DirSouth:
		m.world.decDeg = clampDec(m.world.decDeg - step)
	case device.DirEast:
		m.world.raDeg = normRA(m.world.raDeg + step/math.Max(math.Cos(m.world.decDeg*math.Pi/180), 1e-3))
	case device.DirWest:
		m.world.raDeg = normRA(m.world.raDeg - step/math.Max(math.Cos(m.world.decDeg*math.Pi/180), 1e-3))
	default:
		return fmt.Errorf("unknown direction %q", dir)
	}
	return nil
}

// Nudge moves by an exact angular amount — the dither primitive. The simulated mount applies a
// small backlash penalty in declination, the way a real GEM does, so dither feedback has something
// real to correct.
func (m *Mount) Nudge(_ context.Context, dRAArcsec, dDecArcsec float64) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	if math.Abs(dDecArcsec) > 0 && math.Abs(dDecArcsec) < 3 {
		dDecArcsec *= 0.35 // backlash swallows most of a tiny Dec move
	}
	ra, dec := astro.TangentSky(m.world.raDeg, m.world.decDeg, dRAArcsec/3600, dDecArcsec/3600)
	m.world.raDeg, m.world.decDeg = normRA(ra), clampDec(dec)
	return nil
}

func (m *Mount) SetTracking(_ context.Context, on bool, rate string) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	// Bank whatever accumulated under the outgoing mode, so switching never teleports the stars.
	// Only the UNTRACKED DRIFT is folded into the base pointing: the worm's error stays a live
	// function of worm time, which is what keeps its phase continuous across the pause. Baking that
	// in here as well would double-count it on the very next frame.
	now := m.world.now()
	if m.world.tracking {
		// The worm turned; bank its time. It turns only while the drive does, so stopping tracking
		// PAUSES the periodic error rather than resetting its phase — which is what resetting bootAt
		// here used to do, teleporting the worm whenever anything touched tracking.
		m.world.wormAccum += now.Sub(m.world.wormFrom)
	} else {
		// The sky slid west while the drive was off, and that displacement really happened.
		m.world.raDeg = normRA(m.world.raDeg +
			device.SiderealArcsecPerSec*now.Sub(m.world.driftFrom).Seconds()/3600)
	}
	m.world.wormFrom = now
	m.world.driftFrom = now
	m.world.tracking = on
	if rate != "" {
		m.world.trackingRate = rate
	} else if !on {
		m.world.trackingRate = "off"
	}
	return nil
}

// SetAligned lets a test (or the UI's "simulate an unaligned mount" switch) exercise the refusal
// path that protects real hardware.
func (m *Mount) SetAligned(aligned bool) {
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	m.world.aligned = aligned
}

func clampDec(d float64) float64 {
	if d > 90 {
		return 90
	}
	if d < -90 {
		return -90
	}
	return d
}
