package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The simulated mount's periodic-error-correction table.
//
// This exists so the whole training loop — measure the worm, compute a curve, write it, replay it,
// measure again — can be exercised end to end with no hardware and no sky. That matters more here
// than for most simulated features: the real thing writes state into a mount that outlives the
// session, and the sign, phase and scale conventions it depends on are the kind of thing that is
// either right or catastrophically backwards, with nothing in between.
//
// The world actually APPLIES what is written (see pecCorrectionArcsecLocked), so a curve with the
// wrong sign makes the simulated stars trail worse, exactly as it would on the real mount.

// simSeekDuration is how long the simulated index hunt takes. A real seek turns RA by up to two
// degrees; this one just has to be long enough that the seeking state is observable.
const simSeekDuration = 300 * time.Millisecond

var _ device.PECMount = (*Mount)(nil)

func (m *Mount) PECCaps(_ context.Context) (device.PECCaps, error) {
	if !m.connected {
		return device.PECCaps{}, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()

	bins := len(m.world.pecTable)
	worm := m.world.cfg.PEPeriodSec
	return device.PECCaps{
		Bins:            bins,
		WormPeriodSec:   worm,
		BinSec:          worm / float64(bins),
		LSBArcsecPerSec: device.SiderealArcsecPerSec / m.world.cfg.PECRateScale,
	}, nil
}

func (m *Mount) PECStatus(_ context.Context) (device.PECStatus, error) {
	if !m.connected {
		return device.PECStatus{}, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()

	now := m.world.now()
	if !m.pecSeekDone.IsZero() && !now.Before(m.pecSeekDone) {
		m.world.pecIndexed = true
		m.pecSeekDone = time.Time{}
	}
	return device.PECStatus{
		Supported:  true,
		Indexed:    m.world.pecIndexed,
		Seeking:    !m.pecSeekDone.IsZero(),
		Playing:    m.world.pecPlaying,
		CurrentBin: m.world.pecBinLocked(now),
	}, nil
}

func (m *Mount) PECSeekIndex(ctx context.Context) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	m.pecSeekDone = m.world.now().Add(simSeekDuration)
	m.world.mu.Unlock()

	// A real seek moves the mount, so the caller has to wait for it; mirror that rather than
	// returning instantly and letting the caller believe framing survived.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(simSeekDuration):
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	m.world.pecIndexed = true
	m.pecSeekDone = time.Time{}
	return nil
}

// PECBin is the phase reference a training run folds on.
func (m *Mount) PECBin(_ context.Context) (int, error) {
	if !m.connected {
		return 0, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	return m.world.pecBinLocked(m.world.now()), nil
}

func (m *Mount) PECReadCurve(_ context.Context) ([]int8, error) {
	if !m.connected {
		return nil, device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	return append([]int8(nil), m.world.pecTable...), nil
}

func (m *Mount) PECWriteCurve(_ context.Context, curve []int8) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()
	if len(curve) != len(m.world.pecTable) {
		return fmt.Errorf("curve has %d bins but the mount has %d", len(curve), len(m.world.pecTable))
	}
	copy(m.world.pecTable, curve)
	return nil
}

func (m *Mount) PECPlayback(_ context.Context, on bool) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	m.world.mu.Lock()
	defer m.world.mu.Unlock()

	if on == m.world.pecPlaying {
		return nil
	}
	worm := m.world.wormElapsedLocked(m.world.now())
	if on {
		// Start the integral here, so enabling playback corrects from now on rather than
		// retroactively jumping by everything the table would have accumulated since the index.
		m.world.pecPlayFromWorm = worm
	} else {
		// Stopping playback does not undo the correction already applied — that motion happened.
		// Bank it into the base pointing so the stars stay where they are.
		m.world.raDeg = normRA(m.world.raDeg + m.world.pecCorrectionArcsecLocked(m.world.now())/3600)
	}
	m.world.pecPlaying = on
	return nil
}

// PECRecordStop is a no-op: the simulated mount has no hand controller to have started a recording
// behind our back. It is implemented so the precondition can be asserted unconditionally.
func (m *Mount) PECRecordStop(_ context.Context) error {
	if !m.connected {
		return device.ErrNotConnected
	}
	return nil
}
