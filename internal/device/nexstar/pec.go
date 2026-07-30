package nexstar

import (
	"context"
	"fmt"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Periodic-error correction: reading, writing and replaying the mount's worm-error table.
//
// The table is a ring of signed bytes, one per bin of a single RA worm revolution, and each byte is a
// RATE correction the motor adds while it turns through that bin — not a position offset. The mount
// replays it forever, phase-locked to a mechanical index mark, with no computer involved. That is what
// makes it the right correction for a one-camera rig: nothing can guide during a sub, but the mount can
// correct itself.
//
// Four things about this table are worth knowing before touching it:
//
//   - It is addressed through the same `P` pass-through frame as jog and dither, aimed at the RA motor.
//   - Celestron overloads ONE read command for the data and its metadata, distinguished by a selector
//     byte, so an un-offset read of bin 0 answers with the bin COUNT.
//   - The rate scale changed between mount generations and only the model byte tells them apart.
//     Guessing scales the whole correction by two, which reads as a half-fixed mount rather than a bug.
//   - Replies are raw binary, so they are read by length (see rawBinaryLocked). A bin holding 35 is
//     '#', and scanning for a terminator would desynchronise the port on that value alone.
//
// Nothing here decides WHAT to write — that is internal/pec's job. This file only moves bytes, verifies
// they landed, and refuses to guess.

// pecSeekTimeout bounds the index hunt. Celestron's seek moves RA by up to two degrees and normally
// completes in well under a minute; "AVX stuck on Seeking PEC Index" is a documented failure, so the
// hunt gets a hard deadline and an abort rather than the chance to hang a night.
// (var, not const, so tests can exercise the timeout without waiting three minutes.)
var pecSeekTimeout = 180 * time.Second

// pecSeekPoll is how often the index is checked while seeking.
var pecSeekPoll = time.Second

// pecWriteSettle is the pause between consecutive bin writes. The motor controller acknowledges each
// one, but firmware reports suggest back-to-back writes can be dropped; 20 ms across 88 bins costs
// under two seconds and buys a table that reads back the way it was written. (var, so tests can
// zero it.)
var pecWriteSettle = 20 * time.Millisecond

var _ device.PECMount = (*Mount)(nil)

// PECCaps reports the shape of this mount's PEC table. The bin count comes from the mount; the rate
// scale and worm length are derived from its model byte, because the protocol offers no way to ask.
func (m *Mount) PECCaps(ctx context.Context) (device.PECCaps, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.PECCaps{}, device.ErrNotConnected
	}
	bins, err := m.pecBinCountLocked()
	if err != nil {
		return device.PECCaps{}, err
	}
	wormArcsec := pecWormArcsec(m.modelCode)
	wormSec := wormArcsec / siderealArcsecPerSec
	return device.PECCaps{
		Bins:            bins,
		WormPeriodSec:   wormSec,
		BinSec:          wormSec / float64(bins),
		LSBArcsecPerSec: siderealArcsecPerSec / pecRateScale(m.modelCode),
	}, nil
}

// pecBinCountLocked asks how many bins the table has. Caller holds m.mu.
func (m *Mount) pecBinCountLocked() (int, error) {
	reply, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcPECReadData, []byte{pecCountSelector}, 1), 1)
	if err != nil {
		return 0, fmt.Errorf("read PEC bin count: %w", err)
	}
	bins := int(reply[0])
	if bins <= 0 {
		return 0, fmt.Errorf("mount reports %d PEC bins, which cannot be right", bins)
	}
	return bins, nil
}

// PECStatus reports where the worm is and what the mount is doing about it.
func (m *Mount) PECStatus(ctx context.Context) (device.PECStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.PECStatus{}, device.ErrNotConnected
	}
	st := device.PECStatus{Supported: true, Seeking: m.pecSeeking, Playing: m.pecPlaying}

	indexed, err := m.pecIndexedLocked()
	if err != nil {
		return device.PECStatus{}, err
	}
	st.Indexed = indexed
	if indexed {
		// The seek is over the moment the index is found; the mount will not tell us, so infer it.
		m.pecSeeking = false
		st.Seeking = false
	}

	bin, err := m.pecBinLocked()
	if err != nil {
		return device.PECStatus{}, err
	}
	st.CurrentBin = bin
	return st, nil
}

func (m *Mount) pecIndexedLocked() (bool, error) {
	reply, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcAtIndex, nil, 1), 1)
	if err != nil {
		return false, fmt.Errorf("read PEC index state: %w", err)
	}
	return reply[0] == 0xFF, nil
}

func (m *Mount) pecBinLocked() (int, error) {
	reply, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcPECBin, nil, 1), 1)
	if err != nil {
		return 0, fmt.Errorf("read current PEC bin: %w", err)
	}
	return int(reply[0]), nil
}

// PECBin reports which bin the worm is turning through right now. This is the phase reference the
// training loop folds on — and folding on the mount's own counter means any constant off-by-one
// between the reported index and the one playback uses CANCELS between measurement and write.
func (m *Mount) PECBin(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return 0, device.ErrNotConnected
	}
	return m.pecBinLocked()
}

// PECSeekIndex hunts for the worm's index mark and waits for it.
//
// This MOVES the mount, by up to two degrees in RA, so it belongs before framing and before a guide
// star is chosen — never in the middle of a run. The mutex is released between polls: holding it for
// three minutes would block every other mount command including the abort.
func (m *Mount) PECSeekIndex(ctx context.Context) error {
	m.mu.Lock()
	if m.port == nil {
		m.mu.Unlock()
		return device.ErrNotConnected
	}
	if _, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcSeekIndex, nil, 0), 0); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("start PEC index seek: %w", err)
	}
	m.pecSeeking = true
	m.mu.Unlock()

	deadline := time.Now().Add(pecSeekTimeout)
	for {
		m.mu.Lock()
		indexed, err := m.pecIndexedLocked()
		if err == nil && indexed {
			m.pecSeeking = false
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			// Leave the mount stopped rather than seeking forever — a stuck seek is a reported AVX
			// failure, and a hunting mount cannot be used for anything else.
			_ = m.Abort(ctx)
			m.mu.Lock()
			m.pecSeeking = false
			m.mu.Unlock()
			return fmt.Errorf("the mount did not find its PEC index within %s — "+
				"seek it from the hand controller, or power-cycle the mount and try again", pecSeekTimeout)
		}
		select {
		case <-ctx.Done():
			_ = m.Abort(ctx)
			return ctx.Err()
		case <-time.After(pecSeekPoll):
		}
	}
}

// PECReadCurve reads the whole table, bin by bin — the protocol has no bulk read.
func (m *Mount) PECReadCurve(ctx context.Context) ([]int8, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return nil, device.ErrNotConnected
	}
	bins, err := m.pecBinCountLocked()
	if err != nil {
		return nil, err
	}
	curve := make([]int8, bins)
	for i := range curve {
		v, err := m.pecReadBinLocked(i)
		if err != nil {
			return nil, err
		}
		curve[i] = v
	}
	return curve, nil
}

func (m *Mount) pecReadBinLocked(i int) (int8, error) {
	reply, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcPECReadData, []byte{pecBinOffset + byte(i)}, 1), 1)
	if err != nil {
		return 0, fmt.Errorf("read PEC bin %d: %w", i, err)
	}
	return int8(reply[0]), nil
}

// PECWriteCurve writes the whole table and proves every bin landed.
//
// The read-back is not belt-and-braces: this is the one operation in the app that changes hardware
// state which outlives the session, and a table half-written by a dropped byte tracks WORSE than no
// table at all. A mismatch that survives one retry aborts the write rather than leaving the mount in a
// state nobody measured.
func (m *Mount) PECWriteCurve(ctx context.Context, curve []int8) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	bins, err := m.pecBinCountLocked()
	if err != nil {
		return err
	}
	if len(curve) != bins {
		return fmt.Errorf("curve has %d bins but the mount has %d", len(curve), bins)
	}
	for i, want := range curve {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.pecWriteBinVerifiedLocked(i, want); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mount) pecWriteBinVerifiedLocked(i int, want int8) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		// A signed byte converts to its two's complement directly in Go. Do NOT reproduce INDI's
		// `(v < 127) ? v : 256 - v`, which only lands correctly by way of C's char conversion.
		frame := passthrough(axisAzmRA, mcPECWriteData, []byte{pecBinOffset + byte(i), byte(want)}, 0)
		if _, err := m.rawBinaryLocked(frame, 0); err != nil {
			lastErr = fmt.Errorf("write PEC bin %d: %w", i, err)
			continue
		}
		time.Sleep(pecWriteSettle)

		got, err := m.pecReadBinLocked(i)
		if err != nil {
			lastErr = err
			continue
		}
		if got == want {
			return nil
		}
		lastErr = fmt.Errorf("PEC bin %d read back as %d after writing %d", i, got, want)
	}
	return lastErr
}

// PECPlayback starts or stops replaying the stored table.
//
// Playback must be OFF while the error is being measured: with it on, what the camera sees is the
// RESIDUAL, and a curve computed from that silently under-corrects.
func (m *Mount) PECPlayback(ctx context.Context, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	payload := byte(0)
	if on {
		payload = 1
	}
	if _, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcPECPlayback, []byte{payload}, 0), 0); err != nil {
		return fmt.Errorf("set PEC playback %v: %w", on, err)
	}
	m.pecPlaying = on
	return nil
}

// PECRecordStop cancels any recording the mount started on its own.
//
// It is sent unconditionally before a measurement run. A record session begun from the hand controller
// is invisible over the wire and would quietly overwrite the table with whatever the mount sees while
// we measure — destroying both the run and the user's existing curve.
func (m *Mount) PECRecordStop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	if _, err := m.rawBinaryLocked(passthrough(axisAzmRA, mcPECRecordStop, nil, 0), 0); err != nil {
		return fmt.Errorf("stop PEC recording: %w", err)
	}
	return nil
}
