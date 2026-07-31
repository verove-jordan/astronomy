package nexstar

import (
	"fmt"
	"time"
)

// Getting the link back after it goes away.
//
// Over a night the cable gets nudged, a hub renegotiates, or the adapter re-enumerates — and macOS
// then hands the same hand controller back under a DIFFERENT /dev/cu.usbserial-XXXX, because that
// suffix is a USB location id, not a device identity. So a reconnect cannot simply reopen the path
// it remembers: it re-scans, and proves it found the same mount by talking to it.
//
// Identity is proved by handshake plus the firmware and model it reports, deliberately NOT by the
// USB serial number. Reading that would need go.bug.st/serial's enumerator sub-package, which on
// macOS is IOKit and therefore cgo — banned repo-wide (serial.go) — and the Prolific bridge inside a
// NexStar+ typically carries no serial number anyway. Asking the mount who it is proves the MOUNT is
// the same, which is the thing that actually matters; the cable is not what we care about.

const (
	// reconnectBackoffMin is the first pause. Short, because most drops are a re-enumeration that is
	// already over by the time we notice.
	reconnectBackoffMin = 250 * time.Millisecond
	// reconnectBackoffMax caps the pause. Half a minute, not five: the user may replug at any moment,
	// and a long backoff spends the rest of the night not noticing.
	reconnectBackoffMax = 30 * time.Second
	// reconnectJitter spreads retries so a reconnect never lands in lockstep with whatever else on the
	// machine is also retrying.
	reconnectJitter = 0.2
)

// linkLostLocked handles a command that failed because the descriptor is finished.
//
// It makes exactly ONE immediate reopen attempt and never sleeps: the backoff runs in its own
// goroutine, outside the mutex, because a backoff held under the lock would put Abort — the STOP
// button — behind a thirty-second sleep.
//
// Caller holds m.mu.
func (m *Mount) linkLostLocked(cause error) error {
	if m.closing || m.connecting {
		return cause
	}
	if m.port != nil {
		_ = m.port.Close()
		m.port = nil
	}
	m.stats.lastErr = cause

	if err := m.reopenLocked(); err == nil {
		// Back already — but the command that failed still has an unknown outcome, so it is reported
		// rather than silently retried. The caller (or the next poll) decides what to do about it.
		return fmt.Errorf("%w (the link was reopened on %s; the command's outcome is unknown)", cause, m.path)
	}
	m.startReconnectLocked()
	return cause
}

// startReconnectLocked launches the backoff loop if one is not already running.
func (m *Mount) startReconnectLocked() {
	if m.reconnecting || m.closing || m.stopped == nil {
		return
	}
	m.reconnecting = true
	go m.reconnectLoop(m.stopped)
}

// reconnectLoop retries forever, with backoff, until the mount is back or the driver is closed.
//
// Forever is deliberate. A cable knocked at 3am should be found again at 3.01am, not leave the rest
// of the night dark because an attempt budget ran out.
func (m *Mount) reconnectLoop(done <-chan struct{}) {
	backoff := reconnectBackoffMin
	for {
		select {
		case <-done:
			m.mu.Lock()
			m.reconnecting = false
			m.mu.Unlock()
			return
		default:
		}

		m.sleep(m.jitter(backoff))

		m.mu.Lock()
		if m.closing || m.port != nil {
			m.reconnecting = false
			m.mu.Unlock()
			return
		}
		err := m.reopenLocked()
		if err == nil {
			m.reconnecting = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		if backoff *= 2; backoff > reconnectBackoffMax {
			backoff = reconnectBackoffMax
		}
	}
}

// jitter spreads a backoff by ±20 %.
func (m *Mount) jitter(d time.Duration) time.Duration {
	span := float64(d) * reconnectJitter
	return time.Duration(float64(d) - span + 2*span*m.rnd.Float64())
}

// reopenLocked tries every candidate path until one answers as the mount we had.
//
// Caller holds m.mu.
func (m *Mount) reopenLocked() error {
	for _, path := range m.reconnectCandidatesLocked() {
		port, err := m.opener(path)
		if err != nil {
			// Recorded rather than swallowed: "another program holds the port" and "there is no port"
			// send the user to completely different places, and this is the only place that knows.
			m.stats.lastErr = fmt.Errorf("reopen %s: %w", path, err)
			continue
		}
		m.port = port
		m.path = path
		_ = port.SetReadTimeout(replyTimeout)

		m.connecting = true
		hsErr := m.withTimeout(reopenTimeout, func() error {
			if err := m.handshakeLocked(reopenAttempts); err != nil {
				return err
			}
			return m.proveIdentityLocked()
		})
		m.connecting = false
		if hsErr != nil {
			m.stats.lastErr = hsErr
			_ = port.Close()
			m.port = nil
			continue
		}

		m.afterReopenLocked()
		return nil
	}
	return ErrLinkGone
}

// reconnectCandidatesLocked orders the ports worth trying: the one we had (a re-enumeration usually
// reuses it), then any other adapter, so a hand controller moved to a different USB socket is still
// found. Every candidate is proved by handshake before it is believed, so a generous list is safe.
func (m *Mount) reconnectCandidatesLocked() []string {
	out := []string{}
	if m.path != "" {
		out = append(out, m.path)
	}
	list := m.candidates
	if list == nil {
		list = ListPorts
	}
	var others []string
	for _, p := range list() {
		if p.Path == m.path {
			continue
		}
		if p.Likely {
			out = append(out, p.Path)
		} else {
			others = append(others, p.Path)
		}
	}
	return append(out, others...)
}

// proveIdentityLocked checks that whatever answered is the mount we were talking to.
//
// Without this, a reconnect that lands on the observatory's focuser or a USB-serial GPS would
// "succeed" and then quietly answer every question with nonsense.
func (m *Mount) proveIdentityLocked() error {
	v, err := m.commandLocked("V")
	if err != nil {
		return fmt.Errorf("read firmware after reconnect: %w", err)
	}
	mm, err := m.commandLocked("m")
	if err != nil {
		return fmt.Errorf("read model after reconnect: %w", err)
	}
	firmware, model := parseVersion(v), parseModel(mm)

	if m.model != "" && model != m.model {
		return fmt.Errorf("%s answered as %q, not the %q we were connected to", m.path, model, m.model)
	}
	if m.firmware != "" && firmware != m.firmware {
		return fmt.Errorf("%s reports firmware %s, not the %s we were connected to", m.path, firmware, m.firmware)
	}
	m.model, m.modelCode = model, parseModelCode(mm)
	m.firmware = firmware
	return nil
}

// afterReopenLocked runs the moment the link is proved and BEFORE the port is available to anyone
// else. Order matters here more than anywhere else in the driver.
//
// Caller holds m.mu.
func (m *Mount) afterReopenLocked() {
	m.stats.reconnects++
	m.stats.connectedAt = m.clock()
	m.stats.lastOK = m.clock()

	// First, always: halt anything that was moving when the cable went. The motor controller did not
	// stop because the link did, and this is the only moment we can be sure nothing else is using the
	// port. Everything below can wait; a slewing mount cannot.
	m.flushStopsLocked()

	// A reconnect can mean a power cycle rather than a cable. If the mount used to consider itself
	// aligned and no longer does, its alignment is gone and a GoTo would point somewhere arbitrary —
	// so the driver refuses until a human re-aligns instead of quietly slewing.
	if reply, err := m.commandLocked("J"); err == nil && !parseAligned(reply) {
		m.alignmentStale = true
	}

	// The mount cannot be asked whether PEC is playing, and after a power cycle it comes up not
	// playing and unindexed. Assume the safe direction and do NOT re-enable it: replaying a curve
	// against an unknown index phase tracks WORSE than no curve at all.
	m.pecPlaying, m.pecSeeking = false, false
}
