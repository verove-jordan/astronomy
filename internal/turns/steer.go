package turns

import "strings"

// The steer mailbox is a non-blocking side-channel, distinct from the blocking confirm/ask rendezvous
// (awaitConfirm/resolveConfirm): a long-running producer (the finish supervisor) drains it between
// iterations to pick up free-text nudges ("boost saturation") and a sticky stop request ("keep this
// one"), without ever blocking the loop.

// postMessage queues a free-text nudge and/or sets the sticky stop flag.
func (s *session) postMessage(text string, stop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := strings.TrimSpace(text); t != "" {
		s.steerMsgs = append(s.steerMsgs, t)
	}
	if stop {
		s.steerStop = true
	}
}

// drainMessages returns and clears the queued nudges; the stop flag is sticky (read, not cleared) so a
// producer that polls again still sees it.
func (s *session) drainMessages() (texts []string, stop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	texts, stop, s.steerMsgs = s.steerMsgs, s.steerStop, nil
	return texts, stop
}

// PostMessage delivers a free-text nudge and/or a stop request to a turn. Returns false if unknown.
func (h *Sessions) PostMessage(turnID, text string, stop bool) bool {
	s, ok := h.lookup(turnID)
	if !ok {
		return false
	}
	s.postMessage(text, stop)
	return true
}

// DrainMessages returns the queued nudges (cleared) and the sticky stop flag for a turn. Unknown turn →
// (nil, false).
func (h *Sessions) DrainMessages(turnID string) (texts []string, stop bool) {
	if s, ok := h.lookup(turnID); ok {
		return s.drainMessages()
	}
	return nil, false
}
