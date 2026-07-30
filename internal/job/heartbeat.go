package job

import (
	"fmt"
	"sync"
	"time"
)

// Heartbeat cadence: a beat fires only after silentAfter of stream silence, then repeats every
// repeatEvery while the silence lasts. The ticker itself runs at heartbeatTick so a beat lands
// within one tick of becoming due.
const (
	heartbeatTick   = 15 * time.Second
	heartbeatSilent = 45 * time.Second
	heartbeatRepeat = 30 * time.Second
)

// heartbeat publishes proof-of-life for a running job whose progress stream has gone quiet — the
// CPU-only AI denoise can be silent for an hour and used to read as a hang. Every progress event
// touches it; a per-job ticker asks beat() whether a "still running" line is due. Lines carry the
// job's CURRENT step/pct (never zero — the frontend applies progress unconditionally) and go to
// SSE + stdout only, never the persisted log ring. The clock is injectable for tests.
type heartbeat struct {
	mu        sync.Mutex
	now       func() time.Time
	touched   bool      // a beat before the first progress event would publish pct 0 (bar yank)
	last      time.Time // last progress event seen
	lastBeat  time.Time // last heartbeat published (zeroed by fresh output)
	stepStart time.Time // when the current step began
	step      string
	pct       int
	// enrich (optional) appends live resource context ("cpu 10.8 cores · rss 6.7 GB") supplied by
	// the engine monitor; called outside sections that could re-enter this mutex.
	enrich func() string
}

func newHeartbeat(now func() time.Time) *heartbeat {
	if now == nil {
		now = time.Now
	}
	t := now()
	return &heartbeat{now: now, last: t, stepStart: t}
}

// touch records a progress event; a step change restarts the per-step clock, and any fresh output
// resets the repeat cadence so the next beat again waits the full silence window.
func (h *heartbeat) touch(step string, pct int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.now()
	h.touched = true
	h.last = t
	h.pct = pct
	if step != "" && step != h.step {
		h.step, h.stepStart = step, t
	}
	h.lastBeat = time.Time{}
}

// beat returns the "still running" line when one is due (first after heartbeatSilent of silence,
// then every heartbeatRepeat), with the current step/pct to publish it at; "" when not due.
func (h *heartbeat) beat() (line, step string, pct int) {
	h.mu.Lock()
	t := h.now()
	quiet := t.Sub(h.last)
	due := h.touched && quiet >= heartbeatSilent &&
		(h.lastBeat.IsZero() || t.Sub(h.lastBeat) >= heartbeatRepeat)
	if !due {
		h.mu.Unlock()
		return "", "", 0
	}
	h.lastBeat = t
	name := h.step
	if name == "" {
		name = "processing"
	}
	line = fmt.Sprintf("still running: %s — %s into this step, no output for %s",
		name, t.Sub(h.stepStart).Round(time.Second), quiet.Round(time.Second))
	step, pct = h.step, h.pct
	enrich := h.enrich
	h.mu.Unlock()

	if enrich != nil {
		if extra := enrich(); extra != "" {
			line += " · " + extra
		}
	}
	return line, step, pct
}
