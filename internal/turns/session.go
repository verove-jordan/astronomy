// Package turns is the transport for live "turns" — an event backlog + SSE fan-out plus a confirm/ask
// rendezvous and a free-text steer mailbox, keyed by turn id. It is a leaf package (stdlib only) so both
// the AstroAgent loop (internal/agent) and the processing engine (internal/pipeline via internal/job)
// can drive a turn without an import cycle: the agent streams a ReAct turn here, and a supervised
// finish streams its per-iteration passes here so both render in the same chat UI.
package turns

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Event is one streamed step of a turn, consumed by the SSE endpoint and rendered as the chat activity
// log. Kind is one of: thinking | tool_call | tool_result | confirm | ask | final | error | done.
type Event struct {
	Kind     string   `json:"kind"`
	Step     int      `json:"step,omitempty"`     // 1-based loop step
	Text     string   `json:"text,omitempty"`     // thought (thinking) / answer (final) / message (error)
	Tool     string   `json:"tool,omitempty"`     // tool_call / tool_result
	Args     string   `json:"args,omitempty"`     // tool_call: the JSON args, for display
	Output   string   `json:"output,omitempty"`   // tool_result: the observation
	IsError  bool     `json:"is_error,omitempty"` // tool_result failed
	Mutating bool     `json:"mutating,omitempty"` // tool_call/confirm: the tool changes state
	CallID   string   `json:"call_id,omitempty"`  // confirm/ask: id the UI echoes to approve/reject/choose
	Question string   `json:"question,omitempty"` // confirm/ask: what the user is asked
	Options  []Option `json:"options,omitempty"`  // ask/multi-choice: choices (empty → approve/reject)
	Preview  string   `json:"preview,omitempty"`  // server file path of a rendered pass (frontend wraps with fileUrl)
}

// Option is one choice offered to the user (a fix to apply, a folder to process, a step to approve).
type Option struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// confirmReply is the user's answer to a confirm/ask event.
type confirmReply struct {
	Approve bool
	Choice  string // chosen option id (for ask / multi-choice)
}

// session is one in-flight turn: an event backlog + live fan-out, a pending-confirm rendezvous so a
// producer can block until the user approves an action or picks an option, and a non-blocking steer
// mailbox so the user can nudge a long-running producer (free text) or ask it to stop between steps.
type session struct {
	mu        sync.Mutex
	events    []Event
	subs      []chan Event
	pending   map[string]chan confirmReply
	steerMsgs []string // queued free-text nudges, drained by the producer between steps
	steerStop bool     // sticky: the user asked the producer to stop and keep the best result
	finished  bool
}

func newSession() *session {
	return &session{pending: map[string]chan confirmReply{}}
}

// publish appends an event to the backlog and fans it out to live subscribers (non-blocking, so a slow
// browser never stalls the producer). A "done" event marks the turn finished.
func (s *session) publish(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if e.Kind == "done" {
		s.finished = true
	}
	for _, ch := range s.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// subscribe returns the current backlog (snapshotted atomically with the subscription) plus a live
// channel for subsequent events and an unsubscribe closure. The SSE handler sends the backlog first,
// then follows the channel — so an event is delivered exactly once regardless of connect timing.
func (s *session) subscribe() (backlog []Event, live <-chan Event, cancel func()) {
	ch := make(chan Event, 64)
	s.mu.Lock()
	backlog = append([]Event(nil), s.events...)
	finished := s.finished
	s.subs = append(s.subs, ch)
	s.mu.Unlock()
	if finished {
		close(ch) // nothing more will come; let the reader drain the backlog and stop
	}
	return backlog, ch, func() { s.unsubscribe(ch) }
}

func (s *session) unsubscribe(ch chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.subs {
		if c == ch {
			s.subs = append(s.subs[:i], s.subs[i+1:]...)
			return
		}
	}
}

// awaitConfirm blocks until the user answers the confirm/ask with the given callID, or ctx is
// cancelled. ok=false means the turn was cancelled before an answer arrived.
func (s *session) awaitConfirm(ctx context.Context, callID string) (confirmReply, bool) {
	ch := make(chan confirmReply, 1)
	s.mu.Lock()
	s.pending[callID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, callID)
		s.mu.Unlock()
	}()
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		return confirmReply{}, false
	}
}

// resolveConfirm delivers the user's answer to a waiting confirm/ask. Returns false if no such request
// is pending (already answered, expired, or unknown).
func (s *session) resolveConfirm(callID string, r confirmReply) bool {
	s.mu.Lock()
	ch, ok := s.pending[callID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- r:
		return true
	default:
		return false
	}
}

// reapGrace is how long a finished turn's backlog is kept so a reconnecting SSE reader can still fetch
// the full step history before the session is garbage-collected.
const reapGrace = 60 * time.Second

// Sessions is the hub of live turns, keyed by turn id. It mirrors job.Manager's subscribe/publish so the
// SSE transport and the client stream match the existing job-events pattern.
type Sessions struct {
	mu    sync.Mutex
	m     map[string]*session
	seq   int64
	epoch string // per-process nonce so turn ids never collide across a server restart
}

// NewSessions returns an empty turn hub. Its epoch is a per-process nonce mixed into every turn id:
// the sequence counter alone restarts at 1 on every server boot (air hot-reload, crash recovery,
// deploy), so a fresh process would re-mint "t1", "t2", … and the frontend — which persists agent
// conversations in IndexedDB keyed by turn id — would bind a NEW job's live panel to a PREVIOUS
// run's stale conversation. The epoch makes each boot's ids unique (t<epoch>-<n>).
func NewSessions() *Sessions {
	return &Sessions{m: map[string]*session{}, epoch: newEpoch()}
}

// newEpoch returns a short, monotonic-ish, collision-resistant per-process token. It avoids adding a
// dependency (this is a stdlib-only leaf package) by base-36 encoding the boot nanotime — distinct
// per process start, and never re-used because the counter is appended.
func newEpoch() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// Start mints a new (process-unique) turn id and creates its session. The caller runs the turn's
// loop, streaming steps via Publish and blocking on Await for confirmations.
func (h *Sessions) Start() string {
	id := fmt.Sprintf("t%s-%d", h.epoch, atomic.AddInt64(&h.seq, 1))
	s := newSession()
	h.mu.Lock()
	h.m[id] = s
	h.mu.Unlock()
	return id
}

// Publish streams one event to a turn (no-op if the turn is unknown).
func (h *Sessions) Publish(turnID string, e Event) {
	if s, ok := h.lookup(turnID); ok {
		s.publish(e)
	}
}

// Await blocks until the user answers the confirm/ask with callID for this turn. ok=false → the turn is
// unknown or was cancelled before an answer arrived.
func (h *Sessions) Await(ctx context.Context, turnID, callID string) (approve bool, choice string, ok bool) {
	s, found := h.lookup(turnID)
	if !found {
		return false, "", false
	}
	r, got := s.awaitConfirm(ctx, callID)
	return r.Approve, r.Choice, got
}

// Finish emits the turn's terminal "done" event and schedules the session for reaping after a grace
// period (so a late reader still sees the backlog).
func (h *Sessions) Finish(turnID string) {
	h.Publish(turnID, Event{Kind: "done"})
	time.AfterFunc(reapGrace, func() { h.reap(turnID) })
}

func (h *Sessions) lookup(turnID string) (*session, bool) {
	h.mu.Lock()
	s, ok := h.m[turnID]
	h.mu.Unlock()
	return s, ok
}

// Subscribe attaches an SSE reader to a turn: it returns the backlog, a live channel and an
// unsubscribe closure. ok=false if the turn id is unknown (never started or already reaped).
func (h *Sessions) Subscribe(turnID string) (backlog []Event, live <-chan Event, cancel func(), ok bool) {
	s, ok := h.lookup(turnID)
	if !ok {
		return nil, nil, nil, false
	}
	b, l, c := s.subscribe()
	return b, l, c, true
}

// Resolve answers a pending confirm/ask for a turn. Returns false if the turn or request is unknown.
func (h *Sessions) Resolve(turnID, callID string, approve bool, choice string) bool {
	s, ok := h.lookup(turnID)
	if !ok {
		return false
	}
	return s.resolveConfirm(callID, confirmReply{Approve: approve, Choice: choice})
}

// reap removes a finished turn after a grace period so a late/reconnecting reader can still fetch the
// backlog; called from Finish once the turn's producer exits.
func (h *Sessions) reap(turnID string) {
	h.mu.Lock()
	delete(h.m, turnID)
	h.mu.Unlock()
}
