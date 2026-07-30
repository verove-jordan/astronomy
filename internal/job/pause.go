package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// Resume phases — where a paused run picks up from.
const (
	phasePull     = "pull"     // paused pulling inputs from S3 (before any compute); resume re-pulls then runs
	phaseCompute  = "compute"  // paused mid-stack (manual pause); resume reuses per-channel masters on disk
	phasePush     = "push"     // paused pushing results to S3 (compute done); resume re-pushes the kept result
	phaseTransfer = "transfer" // paused mid standalone transfer; resume re-runs it (sync/size-skip semantics)
)

// Pause causes — WHY a job paused, which decides whether the auto-resume sweep may restart it.
const (
	causeManual = "manual" // the user clicked Pause — stays paused until the user Continues (never auto-resumed)
	causeError  = "error"  // a transient S3 error auto-paused it — the retry sweep resumes it with backoff
	causeThaw   = "thaw"   // waiting for a Glacier restore — the sweep re-checks at a thaw cadence (no attempt cap)
)

// Auto-resume backoff for error-caused pauses: 1,2,4,…,15 min (capped), then give up after maxRetryAttempts.
const (
	baseRetryDelayMs = 60_000  // 1 minute
	maxRetryDelayMs  = 900_000 // 15 minutes
	maxRetryAttempts = 8
)

// Thaw poll cadence for a causeThaw pause: a 2 → 15 min backoff (catch a fast Expedited restore soon, then
// settle to a steady poll), bounded NOT by an attempt count but by a 48 h wall-clock deadline — a Deep
// Archive retrieval can genuinely take that long. The deadline is enforced in run() on re-entry, so a thaw
// that never completes fails clearly instead of polling forever.
const (
	thawPollInitialMs = 120_000          // 2 min
	thawPollMaxMs     = 900_000          // 15 min
	thawDeadlineMs    = 48 * 3600 * 1000 // 48 h
)

// pauseGate is a one-shot cooperative pause signal for one running job. Pause() flips it; the pipeline
// polls requested() at safe boundaries and stops when it is set.
type pauseGate struct{ flag atomic.Bool }

func (g *pauseGate) request()        { g.flag.Store(true) }
func (g *pauseGate) requested() bool { return g.flag.Load() }

// resumeCheckpoint is the job-layer view of a paused run, persisted as the job's `resume` JSONB. Phase says
// where to pick up; RunID/OutDir (compute phase only) let the run reuse the output dir so its already-
// stacked per-channel masters are found and skipped.
type resumeCheckpoint struct {
	Phase  string `json:"phase"`
	RunID  string `json:"run_id,omitempty"`
	OutDir string `json:"out_dir,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Cause is "manual" (user Pause — never auto-resumed), "error" (transient S3 error — auto-resumed with
	// backoff) or "thaw" (waiting for a Glacier restore — auto-resumed at a thaw cadence, no attempt cap).
	// Attempts counts the auto-resume tries so far; NextRetryMs is the earliest wall-clock ms the sweep may
	// re-enqueue it. All zero/empty on a plain manual pause.
	Cause       string `json:"cause,omitempty"`
	Attempts    int    `json:"attempts,omitempty"`
	NextRetryMs int64  `json:"next_retry_ms,omitempty"`
	// DeadlineMs (thaw pauses only) is the wall-clock ms past which a Glacier restore wait gives up: a thaw
	// polls indefinitely (no attempt cap) but never beyond this. 0 on non-thaw pauses.
	DeadlineMs int64 `json:"deadline_ms,omitempty"`
}

// pipelineResume maps the checkpoint to the pipeline's resume handle (nil unless we have a run id/dir to
// reuse — i.e. a compute-phase pause; a pull-phase pause computed nothing yet).
func (c resumeCheckpoint) pipelineResume() *pipeline.ResumeState {
	if c.RunID == "" && c.OutDir == "" {
		return nil
	}
	return &pipeline.ResumeState{RunID: c.RunID, OutDir: c.OutDir}
}

// Pause asks a running job to pause at its next safe boundary. The multi-channel deep-sky path stops after
// the current channel (its stacked master is kept on disk, so Continue reuses it); other modes finish the
// current compute first. Returns false if the job is not running in this process (queued/terminal → Cancel).
func (m *Manager) Pause(id int64) bool {
	m.mu.Lock()
	gate, ok := m.pauses[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	gate.request()
	m.publish(Event{JobID: id, Status: store.JobRunning, Step: "pause requested — will pause at the next safe point"})
	return true
}

// gateFor returns the pause gate registered for a running job (nil if it is not running in this process),
// so the S3 transfer phases can poll the same manual-pause signal the pipeline does.
func (m *Manager) gateFor(id int64) *pauseGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pauses[id]
}

// autoResumeInterval is how often the sweep checks for error-paused jobs whose backoff has elapsed.
const autoResumeInterval = 60 * time.Second

// autoResumePaused periodically restarts ERROR-caused pauses whose backoff has elapsed, until ctx is
// cancelled. Manual pauses are never touched — only a user Continue restarts them.
func (m *Manager) autoResumePaused(ctx context.Context) {
	ticker := time.NewTicker(autoResumeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepPausedForRetry(ctx)
		}
	}
}

// sweepPausedForRetry re-enqueues every error-paused job whose scheduled retry time has passed. Manual
// pauses (and error pauses that have exhausted their attempts, NextRetryMs==0) are skipped.
func (m *Manager) sweepPausedForRetry(ctx context.Context) {
	jobs, err := m.store.ListPausedJobs(ctx)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	for _, j := range jobs {
		var cp resumeCheckpoint
		if json.Unmarshal(j.Resume, &cp) != nil {
			continue
		}
		if !dueForAutoResume(cp, now) {
			continue
		}
		if err := m.Continue(ctx, j.ID); err != nil {
			continue // queue full or no longer paused → retry on the next tick
		}
		step := fmt.Sprintf("auto-resuming after a transfer error (attempt %d/%d)", cp.Attempts, maxRetryAttempts)
		if cp.Cause == causeThaw {
			step = "checking Glacier restore…"
		}
		m.publish(Event{JobID: j.ID, Status: store.JobRunning, Step: step})
	}
}

// dueForAutoResume reports whether a paused checkpoint is due to auto-resume now. An ERROR pause resumes
// when its backoff has elapsed (and it has retries left, NextRetryMs>0); a THAW pause resumes on its poll
// schedule with NO attempt cap (its 48 h deadline is enforced by run() on re-entry). Manual pauses never.
func dueForAutoResume(cp resumeCheckpoint, now int64) bool {
	if cp.NextRetryMs <= 0 || now < cp.NextRetryMs {
		return false
	}
	return cp.Cause == causeError || cp.Cause == causeThaw
}

// Continue resumes a paused job by re-enqueueing the SAME job id onto its lane. run() reads the job's
// resume checkpoint and picks up where it left off. Refuses a job that is not paused.
func (m *Manager) Continue(ctx context.Context, id int64) error {
	j, err := m.store.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if j.Status != store.JobPaused {
		return fmt.Errorf("job %d is %s, not paused", id, j.Status)
	}
	var req RunRequest
	if err := json.Unmarshal(j.Params, &req); err != nil {
		return fmt.Errorf("job %d has invalid params: %w", id, err)
	}
	select {
	case m.laneFor(req) <- id:
		return nil
	default:
		return fmt.Errorf("job queue is full")
	}
}

// laneFor picks the worker lane a run belongs to: its own transfer pool for S3 jobs, the sequential lane
// for chained stacked jobs, else the main pool. Shared by Enqueue and Continue so a resumed job lands where
// a fresh one would.
func (m *Manager) laneFor(req RunRequest) chan int64 {
	switch {
	case req.TierChange != nil:
		// A storage-class/thaw job runs on its own low-cost lane: its work is only Restore/Stat and cheap
		// server-side copies (no local byte I/O), so it never queues behind a multi-hour transfer.
		return m.thawQueue
	case req.Transfer != nil || req.Backup != nil || req.Restore != nil || req.Move != nil:
		return m.xferQueue
	case req.Sequential:
		return m.seqQueue
	default:
		return m.queue
	}
}

// pauseJob parks a running job in the resumable paused state with its checkpoint, keeping progress/step and
// (optionally) the result computed so far. Publishes a paused event so the UI swaps in the Continue action.
func (m *Manager) pauseJob(id int64, cp resumeCheckpoint, result json.RawMessage) {
	blob, err := json.Marshal(cp)
	if err != nil {
		log.Printf("astrostack: marshal resume checkpoint for job %d: %v", id, err)
		return
	}
	if err := m.store.SetJobPaused(context.Background(), id, blob, result, cp.Reason); err != nil {
		log.Printf("astrostack: pause job %d: %v", id, err)
		return
	}
	m.publish(Event{JobID: id, Status: store.JobPaused, Step: cp.Reason, Done: true})
}

// s3Outcome is how a failed S3 transfer phase settles.
type s3Outcome int

const (
	outcomeFail   s3Outcome = iota // permanent error → job fails
	outcomePause                   // transient network error → pause (resumable)
	outcomeCancel                  // user cancelled the run
)

// classifyS3Error decides the outcome of a failed S3 phase from the run-context error and the transfer
// error. Pure (no DB / side effects) so the pause-vs-fail rule is unit-testable. A user cancel wins over
// everything; a MANUAL pause (transfer.ErrPaused) or a transient network error (per s3store.IsRetryable)
// pauses; anything else fails.
func classifyS3Error(ctxErr, err error) s3Outcome {
	if ctxErr != nil || errors.Is(err, context.Canceled) {
		return outcomeCancel
	}
	if errors.Is(err, transfer.ErrPaused) || s3store.IsRetryable(err) {
		return outcomePause
	}
	return outcomeFail
}

// settleS3Error applies classifyS3Error: cancelled → cancelled; a manual pause or transient error → PAUSE
// (resumable, so a flaky endpoint never loses a finished stack); permanent → failed. A MANUAL pause stays
// paused until the user Continues; a transient ERROR pause is scheduled for an auto-resume with escalating
// backoff (priorAttempts carries the count across successive re-pauses). On a push-phase pause the
// computed result is kept so Continue re-pushes it without recomputing.
func (m *Manager) settleS3Error(id int64, runCtx context.Context, phase string, res any, err error, priorAttempts int) {
	switch classifyS3Error(runCtx.Err(), err) {
	case outcomeCancel:
		m.finishTerminal(id, store.JobCancelled, err)
	case outcomePause:
		var result json.RawMessage
		if phase == phasePush {
			result = resultBlob(res)
		}
		m.pauseJob(id, s3PauseCheckpoint(phase, err, priorAttempts), result)
	default:
		m.finishTerminal(id, store.JobFailed, err)
	}
}

// s3PauseCheckpoint builds the resume checkpoint for a paused S3 phase: a manual pause (ErrPaused) parks
// indefinitely; a transient error schedules the next auto-resume with an escalating backoff, until the
// attempts are exhausted (then NextRetryMs stays 0 so the sweep leaves it for a manual Continue).
func s3PauseCheckpoint(phase string, err error, priorAttempts int) resumeCheckpoint {
	if errors.Is(err, transfer.ErrPaused) {
		return resumeCheckpoint{Phase: phase, Cause: causeManual,
			Reason: "paused by you — will stay paused until you continue"}
	}
	attempts := priorAttempts + 1
	cp := resumeCheckpoint{Phase: phase, Cause: causeError, Attempts: attempts, Reason: s3PauseReason(phase, err)}
	if attempts > maxRetryAttempts {
		cp.Reason = "paused — S3 retries exhausted; continue manually when the network is back: " + err.Error()
		return cp // NextRetryMs 0 → the auto-resume sweep skips it
	}
	cp.NextRetryMs = time.Now().UnixMilli() + retryBackoffMs(attempts)
	return cp
}

// thawCheckpoint builds a causeThaw pause: the next poll is scheduled with an escalating 2→15 min backoff
// (carried in Attempts), and DeadlineMs is threaded forward across re-parks so the 48 h give-up clock does
// not reset each poll. phase is where the resumed job re-enters (phaseTransfer for the standalone tier job,
// phasePull for a process run waiting on cold inputs).
func thawCheckpoint(phase string, priorAttempts int, deadlineMs int64) resumeCheckpoint {
	attempts := priorAttempts + 1
	return resumeCheckpoint{
		Phase:       phase,
		Cause:       causeThaw,
		Attempts:    attempts,
		NextRetryMs: time.Now().UnixMilli() + thawBackoffMs(attempts),
		DeadlineMs:  deadlineMs,
		Reason:      thawReason(deadlineMs),
	}
}

// thawDeadlineOr returns the checkpoint's existing thaw deadline, or a fresh now+48h one the first time a
// job parks to thaw (a resume carries the original forward so the wait is bounded from the first request).
func thawDeadlineOr(cp resumeCheckpoint) int64 {
	if cp.DeadlineMs > 0 {
		return cp.DeadlineMs
	}
	return time.Now().UnixMilli() + thawDeadlineMs
}

// thawExpired reports whether a thaw pause has passed its give-up deadline (run() fails such a job).
func thawExpired(cp resumeCheckpoint, now int64) bool {
	return cp.Cause == causeThaw && cp.DeadlineMs > 0 && now > cp.DeadlineMs
}

// thawBackoffMs is the poll backoff for the Nth (1-based) thaw check: 2 min doubling to a 15 min cap.
func thawBackoffMs(attempt int) int64 {
	delay := int64(thawPollInitialMs)
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= thawPollMaxMs {
			return thawPollMaxMs
		}
	}
	return delay
}

// thawReason is the human status shown on a thaw-paused job (with a rough remaining-window hint).
func thawReason(deadlineMs int64) string {
	if deadlineMs > 0 {
		if rem := time.Duration(deadlineMs-time.Now().UnixMilli()) * time.Millisecond; rem > 0 {
			return fmt.Sprintf("thawing from Glacier — auto-checking (retrieval window ~%dh left)", int(rem.Hours()+0.5))
		}
	}
	return "thawing from Glacier — auto-checking"
}

// retryBackoffMs is the exponential backoff for the Nth (1-based) auto-resume attempt, capped.
func retryBackoffMs(attempt int) int64 {
	delay := int64(baseRetryDelayMs)
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxRetryDelayMs {
			return maxRetryDelayMs
		}
	}
	return delay
}

// finishTerminal writes a terminal (failed/cancelled) status with a fresh context so it persists even if
// the run context was cancelled, publishes the done event, and closes any conversation turn. The error
// also lands in the live journal (and stdout) as a ✗ line — the Step field alone never reached the log.
func (m *Manager) finishTerminal(id int64, status string, err error) {
	_ = m.store.FinishJob(context.Background(), id, status, nil, err.Error())
	line := fmt.Sprintf("✗ job %s: %s", status, err.Error())
	m.publish(Event{JobID: id, Status: status, Line: line, Ts: time.Now().UnixMilli()})
	log.Printf("astrostack: job %d %s", id, line)
	m.publish(Event{JobID: id, Status: status, Step: err.Error(), Done: true})
	m.closeTurn(id, status, err.Error())
}

// finishSucceeded writes the successful terminal status + result, publishes done, closes the turn, and
// advances any agent series.
func (m *Manager) finishSucceeded(id int64, p RunRequest, result json.RawMessage) {
	_ = m.store.FinishJob(context.Background(), id, store.JobSucceeded, result, "")
	m.publish(Event{JobID: id, Status: store.JobSucceeded, Progress: 100, Step: "done", Done: true})
	m.closeTurn(id, store.JobSucceeded, "Finished — kept the best pass as the final image.")
	m.maybeContinueSeries(id, p)
}

// resultBlob marshals a pipeline result for persistence; nil stays nil (leave the stored result untouched).
// A json.RawMessage marshals back to its own bytes, so this also works to re-persist a prior result.
// A marshal failure means a non-finite float (encoding/json refuses NaN/±Inf): zero those and retry
// rather than silently storing no result at all — the empty-result half of task #353. (The deepsky
// path normally sanitizes in writeRunJSON already; this covers results that never pass through it,
// e.g. a cancelled run's partial result.)
func resultBlob(res any) json.RawMessage {
	if res == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		if paths := pipeline.SanitizeNonFinite(res); len(paths) > 0 {
			log.Printf("job result contained non-finite numbers (zeroed): %d field(s)", len(paths))
			b, err = json.Marshal(res)
		}
		if err != nil {
			log.Printf("job result not serializable: %v", err)
			return nil
		}
	}
	return b
}

// s3PauseReason is the human message stored on a job auto-paused by a transient S3 error.
func s3PauseReason(phase string, err error) string {
	switch phase {
	case phasePull:
		return "paused — network error pulling inputs from S3 (will resume from here): " + err.Error()
	case phasePush:
		return "paused — network error uploading results to S3 (results kept locally; will re-upload): " + err.Error()
	default:
		return "paused — S3 network error: " + err.Error()
	}
}
