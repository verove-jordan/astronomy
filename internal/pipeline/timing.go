package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// StepTiming is one named step's accumulated wall time, persisted into run.json so runs are
// comparable ("where did the two hours go?") without scraping logs.
type StepTiming struct {
	Step string `json:"step"`
	Ms   int64  `json:"ms"`
}

// stepTimer observes the run's Progress stream (installed as a wrapper around opts.OnProgress at
// the top of Process) and accumulates wall time per step name. Repeated names accumulate — a step
// revisited after a pause/resume or a supervised re-render adds to its bucket. This is THE timing
// mechanism; the stepper only emits human ✓-duration lines.
type stepTimer struct {
	mu      sync.Mutex
	now     func() time.Time
	order   []string
	byStep  map[string]time.Duration
	current string
	started time.Time
}

func newStepTimer(now func() time.Time) *stepTimer {
	if now == nil {
		now = time.Now
	}
	return &stepTimer{now: now, byStep: map[string]time.Duration{}}
}

// observe folds one progress event's step; a change of name closes the previous accumulation.
func (t *stepTimer) observe(step string) {
	if step == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if step == t.current {
		return
	}
	t.closeLocked()
	t.current, t.started = step, t.now()
}

// finish closes the running step and returns the ordered timings.
func (t *stepTimer) finish() []StepTiming {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
	out := make([]StepTiming, 0, len(t.order))
	for _, s := range t.order {
		out = append(out, StepTiming{Step: s, Ms: t.byStep[s].Milliseconds()})
	}
	return out
}

func (t *stepTimer) closeLocked() {
	if t.current == "" {
		return
	}
	if _, seen := t.byStep[t.current]; !seen {
		t.order = append(t.order, t.current)
	}
	t.byStep[t.current] += t.now().Sub(t.started)
	t.current = ""
}

// timingSummary renders the one-line per-step breakdown for the journal
// ("timing: masters 2m10s · stacking L 8m30s · … · total 1h52m"). "" when nothing was timed.
func timingSummary(timings []StepTiming) string {
	if len(timings) == 0 {
		return ""
	}
	var parts []string
	var total time.Duration
	for _, t := range timings {
		d := time.Duration(t.Ms) * time.Millisecond
		total += d
		parts = append(parts, t.Step+" "+d.Round(time.Second).String())
	}
	return fmt.Sprintf("timing: %s · total %s", strings.Join(parts, " · "), total.Round(time.Second))
}
