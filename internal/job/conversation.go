package job

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/turns"
)

// This file bridges a supervised finish to a live, steerable conversation over the shared turn transport
// (internal/turns): the manager mints a turn per supervised/refine job (Enqueue), streams each pass as a
// chat bubble (pipeProg), lets the user nudge/stop it (steerHook) and gates the expensive Tier-C re-stack
// behind a confirmation (confirmHook). Everything degrades to headless when m.turns is nil / turnID=="".

// superviseConfirmTimeout bounds how long the supervised finish waits at an expensive-step confirmation
// before proceeding on its own — so a run whose watcher walked away still completes (as autonomous
// supervision would have) rather than hanging on the Tier-C gate forever.
const superviseConfirmTimeout = 10 * time.Minute

// TurnFor returns the conversation turn id bound to a supervised job (minted in Enqueue), or "" if the
// job has none. The API returns it so the client can watch and steer the supervised finish.
func (m *Manager) TurnFor(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobTurns[id]
}

// closeTurn ends a supervised job's conversation: it unbinds the job→turn mapping (idempotent — a second
// call is a no-op) and, on the first close, publishes a terminal bubble and finishes the turn so the SSE
// stream ends. A job with no turn (non-supervised, or headless) is a no-op.
func (m *Manager) closeTurn(id int64, status, summary string) {
	m.mu.Lock()
	turnID, ok := m.jobTurns[id]
	delete(m.jobTurns, id)
	m.mu.Unlock()
	if !ok || m.turns == nil {
		return
	}
	kind := "final"
	if status == store.JobFailed || status == store.JobCancelled {
		kind = "error"
	}
	m.turns.Publish(turnID, turns.Event{Kind: kind, Text: summary})
	m.turns.Finish(turnID)
}

// steerHook builds the pipeline's between-iterations steer poller for a turn (nil when there is no turn):
// it drains queued free-text nudges (joined) and the sticky stop flag from the conversation mailbox.
func (m *Manager) steerHook(turnID string) func() (string, bool) {
	if turnID == "" || m.turns == nil {
		return nil
	}
	return func() (string, bool) {
		texts, stop := m.turns.DrainMessages(turnID)
		return strings.Join(texts, "; "), stop
	}
}

// confirmHook builds the pipeline's expensive-step gate for a turn (nil when there is no turn): it posts
// an ask bubble to the conversation and blocks (bounded by superviseConfirmTimeout) until the user picks
// an option. A timeout / unknown turn returns ok=false, which the loop treats as "proceed".
func (m *Manager) confirmHook(turnID string) func(context.Context, string, []string) (string, bool) {
	if turnID == "" || m.turns == nil {
		return nil
	}
	return func(ctx context.Context, question string, options []string) (string, bool) {
		callID := fmt.Sprintf("ask-%s-%d", turnID, atomic.AddInt64(&m.confirmSeq, 1))
		opts := make([]turns.Option, len(options))
		for i, o := range options {
			opts[i] = turns.Option{ID: o, Label: o}
		}
		m.turns.Publish(turnID, turns.Event{Kind: "ask", CallID: callID, Question: question, Options: opts})
		cctx, cancel := context.WithTimeout(ctx, superviseConfirmTimeout)
		defer cancel()
		_, choice, ok := m.turns.Await(cctx, turnID, callID)
		return choice, ok
	}
}

// iterSummary renders one supervised-finish pass as a single chat bubble: tier + scores, the model's
// reasoning, and the diagnosed defects.
func iterSummary(rec *postprocess.IterationRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pass %d", rec.Index+1)
	if rec.Tier != "" {
		fmt.Fprintf(&b, " (tier %s)", rec.Tier)
	}
	fmt.Fprintf(&b, " — score %.1f/10 (metrics %.1f, model %.1f)", rec.CombinedScore, rec.DetScore, rec.ModelScore)
	if r := strings.TrimSpace(rec.Reasoning); r != "" {
		b.WriteString("\n")
		b.WriteString(r)
	}
	if len(rec.Defects) > 0 {
		parts := make([]string, 0, len(rec.Defects))
		for _, d := range rec.Defects {
			parts = append(parts, fmt.Sprintf("%s (%s)", d.Kind, d.Severity))
		}
		fmt.Fprintf(&b, "\nDefects: %s", strings.Join(parts, ", "))
	}
	return b.String()
}
