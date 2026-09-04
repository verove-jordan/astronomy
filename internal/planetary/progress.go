// Run-level completion tracking for the job progress bar. Planetary has no fixed step count like
// deep-sky, so the run is budgeted by phase weights (per channel: materialize/calibrate/score/align/
// stack; then the finish) and the per-frame loops tick within the current phase. Emissions are
// throttled to whole-percent changes — every event the manager sees can flush a DB progress write —
// and serialized behind one mutex, because the job progress sink chain is not goroutine-safe while
// the warp/score ticks arrive from parallel workers.
package planetary

import "sync"

// Phase weights within one channel's share of the run (fractions of the channel span). Align dominates
// wall-clock; the exact split only shapes the bar, not the work.
const (
	phaseMaterialize = 0.15
	phaseCalibrate   = 0.10
	phaseScore       = 0.10
	phaseAlign       = 0.50
	phaseStack       = 0.15
	// phaseDrift is the slice of a channel's span spent measuring the capture's drift trajectory
	// before deciding whether it is one panel or a sweep. It reads every frame once, decimated.
	phaseDrift   = 0.08
	finishWeight = 10.0 // percent reserved for co-register/deconvolve/finish after the channels
)

// runProgress tracks overall run completion (0..100) across weighted phases. All methods are safe on
// concurrent callers; a nil onPct makes every emission a no-op (CLI/MCP runs).
type runProgress struct {
	mu       sync.Mutex
	onPct    func(p float64)
	base     float64 // percent completed by finished phases
	span     float64 // current phase's weight in percent
	lastSent int
}

func newRunProgress(onPct func(p float64)) *runProgress {
	return &runProgress{onPct: onPct, lastSent: -1}
}

// phase closes the current phase and opens a new one worth `weight` percent of the whole run.
func (rp *runProgress) phase(weight float64) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.base += rp.span
	rp.span = weight
	rp.emit(rp.base)
}

// tick reports done-of-total within the current phase (concurrent-safe; called per frame).
func (rp *runProgress) tick(done, total int) {
	if total <= 0 {
		return
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.emit(rp.base + rp.span*float64(done)/float64(total))
}

// finish closes the last phase and pins the bar at 100.
func (rp *runProgress) finish() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.base += rp.span
	rp.span = 0
	rp.emit(100)
}

// emit forwards a whole-percent change to onPct. Callers hold rp.mu.
func (rp *runProgress) emit(p float64) {
	if rp.onPct == nil {
		return
	}
	if p > 100 {
		p = 100
	}
	if int(p) == rp.lastSent {
		return
	}
	rp.lastSent = int(p)
	rp.onPct(p)
}
