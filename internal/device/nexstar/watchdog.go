package nexstar

import "time"

// The deadman that stops a moving axis when nobody else will.
//
// A motor controller does not stop because a USB cable was pulled, a browser tab was closed, or a
// laptop went to sleep. It stops when it is told to. Every path in this driver that starts an axis
// therefore registers the frame that would halt it, and this file guarantees that frame is sent —
// on time if the caller forgets, and immediately after a reconnect if the link died mid-move.
//
// That last case is a bug this replaces rather than a hypothetical: both nudgeAxis and PulseGuide
// used to check `if m.port == nil { return nil }` before their stop and report SUCCESS, having left
// the axis running.

const (
	// jogDeadman is how long an axis may run without the caller renewing it.
	//
	// At rate 9 an AVX slews about 3°/s, so four seconds of runaway is roughly twelve degrees —
	// recoverable. Ten seconds is thirty, which on a German equatorial can reach the tripod. The
	// deadman is therefore short and the CLIENT renews it; a generous deadman with no renewal is the
	// trade that breaks a tube.
	jogDeadman = 4 * time.Second
	// stopGrace is added to a move of known length. The stop can wait up to one reply timeout for an
	// in-flight command to release the port, so anything less would fire during a legitimate pulse.
	stopGrace = replyTimeout + time.Second
	// watchdogTick is how often deadlines are examined. Well under jogDeadman, and cheap: the tick
	// touches the port only when something has actually expired.
	watchdogTick = 250 * time.Millisecond
)

// pendingStop is an axis that has been commanded to move and not yet commanded to stop.
type pendingStop struct {
	// frame is the exact rate-0 frame for this axis, built when the move started so the watchdog
	// never has to reconstruct it from state that may since have changed.
	frame    []byte
	deadline time.Time
}

// SetDeadman changes how long an unrenewed jog may run. The device server sets it from the hold the
// UI promises to renew within; zero restores the default.
func (m *Mount) SetDeadman(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = jogDeadman
	}
	m.deadman = d
}

func (m *Mount) deadmanLocked() time.Duration {
	if m.deadman > 0 {
		return m.deadman
	}
	return jogDeadman
}

// armStopLocked records that an axis is moving, and by when it must be stopped.
//
// Re-arming an axis that is already armed simply extends it — that is what a renewal is.
func (m *Mount) armStopLocked(axis int, stop []byte, within time.Duration) {
	if m.stops == nil {
		m.stops = map[int]*pendingStop{}
	}
	m.stops[axis] = &pendingStop{frame: stop, deadline: m.clock().Add(within)}
	m.ensureWatchdogLocked()
}

// disarmStopLocked records that an axis has been stopped.
func (m *Mount) disarmStopLocked(axis int) {
	delete(m.stops, axis)
}

// ensureWatchdogLocked starts the watchdog goroutine the first time an axis moves.
//
// Lazily, not in New: a Mount that is constructed and never connected — which every listing and
// every probe does — must leak nothing, and a goroutine started per construction would.
func (m *Mount) ensureWatchdogLocked() {
	if m.watchdogRunning || m.stopped == nil {
		return
	}
	m.watchdogRunning = true
	done := m.stopped
	go func() {
		t := time.NewTicker(watchdogTick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				m.expireStops()
			}
		}
	}()
}

// expireStops sends the stop for any axis whose deadline has passed.
//
// It is a method rather than an inline loop so tests can drive it directly after advancing the
// injected clock, instead of sleeping and hoping.
func (m *Mount) expireStops() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		// The link is down. The stops stay armed: reopenLocked flushes them before the port is
		// handed back to anyone else, which is the only way a mount that lost its cable mid-slew
		// ever stops.
		return
	}
	now := m.clock()
	for axis, p := range m.stops {
		if now.Before(p.deadline) {
			continue
		}
		// classify() rates a rate-0 frame retryAlways, so this goes through the resynchronisation and
		// retry path rather than being dropped on the first hiccup.
		if _, err := m.sendLocked(p.frame, -1); err != nil {
			// Leave it armed and try again next tick. An axis that will not stop is the one thing
			// worth being stubborn about.
			continue
		}
		delete(m.stops, axis)
	}
}

// flushStopsLocked sends every outstanding stop, and is called by a reconnect BEFORE the port is
// handed back to anything else. If the link dropped while an axis was running, this is the moment
// the mount is brought to a halt.
func (m *Mount) flushStopsLocked() {
	for axis, p := range m.stops {
		if _, err := m.sendLocked(p.frame, -1); err != nil {
			continue
		}
		delete(m.stops, axis)
	}
}

// stopsPending reports how many axes are still armed. Used by the tests and by the soak report.
func (m *Mount) stopsPending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.stops)
}
