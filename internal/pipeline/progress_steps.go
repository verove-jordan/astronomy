package pipeline

import (
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/photom"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// lightStepSlots counts the channel step slots the bar plans for: the light sets with the capture
// night normalized OUT. A multi-night run stacks its per-night groups inside ONE channel step, and a
// single-night run (Session already zero everywhere) keeps the historical set count exactly.
func lightStepSlots(inv *inspect.Inventory) int {
	seen := map[inspect.SetKey]bool{}
	for _, set := range inv.SetsOfType(inspect.Light) {
		k := set.Key
		k.Session = ""
		seen[k] = true
	}
	return len(seen)
}

// stepper serializes a run's named progress steps: each begin() closes the previous step with a
// ✓-duration journal line, advances the bar (clamped to the planned total), announces the new step
// with a ▶ line, and returns the forwarder its tool output streams through. It exists so the
// finish tail — background extraction, the (possibly 90-minute) AI denoise, colour calibration,
// the GIMP composite, StarNet — reads as real steps instead of one silent 92%-pinned "combined".
// Mutex-guarded: step boundaries may be crossed from concurrent goroutines (parallel channels).
type stepper struct {
	mu      sync.Mutex
	report  func(Progress)
	index   int
	total   int
	name    string
	started time.Time
}

func newStepper(report func(Progress), total int) *stepper {
	return &stepper{report: report, total: total}
}

// begin closes the current step (if any) and starts a named one, returning the per-step forwarder
// for Siril/GraXpert/StarNet output. The index clamps at total: an unplanned runtime step (e.g. a
// fallback) pins the bar at the last planned position rather than overflowing it.
func (s *stepper) begin(name string) func(siril.Progress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
	if s.index < s.total {
		s.index++
	}
	s.name, s.started = name, time.Now()
	idx, total := s.index, s.total
	s.report(Progress{Step: name, Index: idx, Total: total})
	s.report(Progress{Step: name, Index: idx, Total: total, Line: "▶ " + name})
	return func(p siril.Progress) {
		if p.Line != "" || p.Sample != nil {
			s.report(Progress{Step: name, Index: idx, Total: total, Line: p.Line, Sample: p.Sample})
		}
	}
}

// finish closes the last step (its ✓-duration line) at the end of the run.
func (s *stepper) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// at opens a FIXED-index step slot without touching the serial cursor: parallel channel waves give
// each channel its own slot, so begin()'s close-the-previous semantics can't interleave across
// goroutines. done() emits the slot's ✓-duration line when the channel completes. The returned
// stepRef pins the slot for mid-step per-session events.
func (s *stepper) at(index int, name string) (fwd func(siril.Progress), done func(), ref stepRef) {
	s.mu.Lock()
	if index > s.total {
		index = s.total
	}
	idx, total := index, s.total
	s.report(Progress{Step: name, Index: idx, Total: total})
	s.report(Progress{Step: name, Index: idx, Total: total, Line: "▶ " + name})
	s.mu.Unlock()
	started := time.Now()
	fwd = func(p siril.Progress) {
		if p.Line != "" || p.Sample != nil {
			s.report(Progress{Step: name, Index: idx, Total: total, Line: p.Line, Sample: p.Sample})
		}
	}
	done = func() {
		s.report(Progress{Step: name, Index: idx, Total: total,
			Line: "✓ " + name + " done in " + time.Since(started).Round(time.Second).String()})
	}
	return fwd, done, stepRef{Name: name, Index: idx, Total: total}
}

// advanceTo moves the serial cursor forward to index (never back), so steps begun after a parallel
// wave continue from the wave's end.
func (s *stepper) advanceTo(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index > s.total {
		index = s.total
	}
	if index > s.index {
		s.index = index
	}
}

// pos is the current step position, for events that ride along mid-step (previews, reused
// channels) without advancing the bar.
func (s *stepper) pos() (index, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index, s.total
}

func (s *stepper) closeLocked() {
	if s.name == "" {
		return
	}
	s.report(Progress{Step: s.name, Index: s.index, Total: s.total,
		Line: "✓ " + s.name + " done in " + time.Since(s.started).Round(time.Second).String()})
	s.name = ""
}

// beginStep starts a named step on the run's stepper. Runs without one (OSC, refine, CLI, and the
// supervised/star-fix re-entries, which must never advance the main bar) fall back to an
// index-less line forwarder — exactly the pre-stepper behavior.
func (o Options) beginStep(name string) func(siril.Progress) {
	if o.steps == nil {
		return o.sirilLines(name)
	}
	return o.steps.begin(name)
}

// stepPos reports the current step position (zeros without a stepper).
func (o Options) stepPos() (index, total int) {
	if o.steps == nil {
		return 0, 0
	}
	return o.steps.pos()
}

// stepAt opens a fixed-index step slot (parallel channel waves); nil-safe like beginStep.
func (o Options) stepAt(index int, name string) (fwd func(siril.Progress), done func(), ref stepRef) {
	if o.steps == nil {
		return o.sirilLines(name), func() {}, stepRef{Name: name}
	}
	return o.steps.at(index, name)
}

// stepRef pins one channel's step slot (name + bar position) so mid-step per-session events emitted
// from a concurrent wave carry the right position instead of riding at whichever step last reported.
// The zero value (re-stack, refine, CLI paths) pins nothing: events ride at the last position, the
// exact pre-existing behavior of those paths' index-less lines.
type stepRef struct {
	Name         string
	Index, Total int
}

// sessionLine emits a journal line attributed to one capture session, pinned at ref's slot — the
// per-session sub-step markers inside a cross-session channel step. It never advances the bar.
func (o Options) sessionLine(ref stepRef, session, line string) {
	o.report(Progress{Step: ref.Name, Index: ref.Index, Total: ref.Total, Session: session, Line: line})
}

// sessionPhotom streams one group's photometric-normalization record live (the job UI's per-night
// ×scale/offset chips), pinned at ref's slot.
func (o Options) sessionPhotom(ref stepRef, session string, rec photom.GroupRecord) {
	o.report(Progress{Step: ref.Name, Index: ref.Index, Total: ref.Total, Session: session, Photom: &rec})
}

// finishSteps closes the stepper's last step; safe without one.
func (o Options) finishSteps() {
	if o.steps != nil {
		o.steps.finish()
	}
}

// finishStepPlan sizes the finish half of the progress bar: the named steps this run's preset
// implies after the per-channel stacks. It reads only the preset and configured tools — no health
// probes — so a runtime soft-fallback may skip a planned step (the bar jumps forward) or add an
// unplanned one (the index clamps): sizing is best-effort and strictly monotonic either way.
func finishStepPlan(opts Options) []string {
	steps := []string{"aligning channels"}
	switch {
	case opts.Preset != nil && opts.Preset.Supervise && opts.Supervisor != nil:
		steps = append(steps, "supervised finish")
	case opts.Gimp != nil && opts.Preset != nil:
		steps = append(steps, "combining channels + background")
		if opts.Preset.ColorDenoiseAI && opts.Graxpert != nil {
			steps = append(steps, "AI colour denoise (GraXpert)")
		}
		steps = append(steps, "colour calibration + stretch", "composite (GIMP)")
		if opts.Preset.StarReduce > 0 && opts.Starnet != nil {
			steps = append(steps, "star reduction (StarNet++)")
		}
		if opts.Preset.AutoFixStars {
			steps = append(steps, "star quality check")
		}
	default:
		steps = append(steps, "combining channels (Siril)")
	}
	return append(steps, "export")
}
