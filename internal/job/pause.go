package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
)

// Resume phases — where a paused run picks up from.
const (
	phasePull    = "pull"    // paused pulling inputs from S3 (before any compute); resume re-pulls then runs
	phaseCompute = "compute" // paused mid-stack (manual pause); resume reuses per-channel masters on disk
	phasePush    = "push"    // paused pushing results to S3 (compute done); resume re-pushes the kept result
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
	case req.Transfer != nil || req.Backup != nil || req.Restore != nil:
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
// everything; a transient network error (per s3store.IsRetryable) pauses; anything else fails.
func classifyS3Error(ctxErr, err error) s3Outcome {
	if ctxErr != nil || errors.Is(err, context.Canceled) {
		return outcomeCancel
	}
	if s3store.IsRetryable(err) {
		return outcomePause
	}
	return outcomeFail
}

// settleS3Error applies classifyS3Error: cancelled → cancelled; transient → PAUSE (resumable, so a flaky
// endpoint never loses a finished stack); permanent → failed. On a push-phase pause the computed result is
// kept so Continue re-pushes it without recomputing.
func (m *Manager) settleS3Error(id int64, runCtx context.Context, phase string, res any, err error) {
	switch classifyS3Error(runCtx.Err(), err) {
	case outcomeCancel:
		m.finishTerminal(id, store.JobCancelled, err)
	case outcomePause:
		var result json.RawMessage
		if phase == phasePush {
			result = resultBlob(res)
		}
		m.pauseJob(id, resumeCheckpoint{Phase: phase, Reason: s3PauseReason(phase, err)}, result)
	default:
		m.finishTerminal(id, store.JobFailed, err)
	}
}

// finishTerminal writes a terminal (failed/cancelled) status with a fresh context so it persists even if
// the run context was cancelled, publishes the done event, and closes any conversation turn.
func (m *Manager) finishTerminal(id int64, status string, err error) {
	_ = m.store.FinishJob(context.Background(), id, status, nil, err.Error())
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
func resultBlob(res any) json.RawMessage {
	if res == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil
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
