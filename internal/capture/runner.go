package capture

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/dither"
)

// The sequencer. One telescope, one session at a time — refusing a second is a feature, not a
// limitation: two sequences fighting over the same filter wheel would silently ruin both.

// ErrSessionRunning is returned when a session is asked for while one is already active.
var ErrSessionRunning = errors.New("a capture session is already running")

// runHandoverTimeout bounds how long Start waits for a stopped run to finish unwinding. Generous:
// the only thing it waits for is one cancelled exposure being abandoned and one row being written,
// and failing early here would mean refusing a session the telescope is perfectly free to shoot.
const runHandoverTimeout = 30 * time.Second

// Recorder persists what the sequencer does. It is an interface so this package stays free of the
// database, and so tests can run a whole session against an in-memory recorder.
type Recorder interface {
	CreateSession(ctx context.Context, req Request, total int) (int64, error)
	UpdateSession(ctx context.Context, id int64, status Status, progress Progress) error
	RecordFrame(ctx context.Context, sessionID int64, frame FrameRecord) error
}

// FrameRecord is one saved exposure, as persisted.
type FrameRecord struct {
	Path       string
	Filter     string
	Type       string
	ExposureUs int64
	Gain       int64
	Offset     int64
	Bin        int
	TempMilliC int
	Panel      string
	StartedAt  time.Time
	Sequence   int
}

// Request starts a session: what to shoot, where to put it, and what it is of.
type Request struct {
	Sequence Sequence `json:"sequence"`
	// Root is the capture directory (inside the data dir; the API validates that). Frames land in
	// <root>/[panel/]<file> so a mosaic tile's frames are already in the folder the stacker looks
	// for.
	Root      string  `json:"root"`
	Object    string  `json:"object"`
	Panel     string  `json:"panel,omitempty"`
	Telescope string  `json:"telescope,omitempty"`
	FocalMM   float64 `json:"focal_mm,omitempty"`
	PixelUm   float64 `json:"pixel_um,omitempty"`

	// RADeg/DecDeg is where this run is pointed (J2000). Written into the frame headers and used as
	// the plate-solve hint, which turns a blind all-sky search into a couple of seconds.
	RADeg  float64 `json:"ra_deg,omitempty"`
	DecDeg float64 `json:"dec_deg,omitempty"`

	// LatDeg/LonDeg/ElevationM is where the telescope stands. The sequencer never uses it — it is
	// recorded with the session so the night's conditions (weather, Moon, sky brightness) are
	// attributable to a place afterwards, when the observer may well have moved on.
	LatDeg     float64 `json:"lat_deg,omitempty"`
	LonDeg     float64 `json:"lon_deg,omitempty"`
	ElevationM float64 `json:"elevation_m,omitempty"`

	MosaicPlanID int64 `json:"mosaic_plan_id,omitempty"`
	TileIndex    *int  `json:"tile_index,omitempty"`

	// ResumedFrom is the session this run is finishing, when it is finishing one. It rides along in
	// the persisted request rather than in a column of its own: it is provenance for a human reading
	// the journal, not something the sequencer branches on.
	ResumedFrom int64 `json:"resumed_from,omitempty"`

	// Dither settings apply to every step that asks for dithering.
	DitherRadiusPx float64 `json:"dither_radius_px,omitempty"`
	// ImageScaleArcsecPx converts a dither in pixels into the mount nudge in arcseconds. Without
	// it dithering is skipped rather than guessed at.
	ImageScaleArcsecPx float64 `json:"image_scale_arcsec_px,omitempty"`
}

// Runner owns the active session.
type Runner struct {
	client   *Client
	recorder Recorder

	// startMu serialises Start itself, so the handover below cannot be raced by a second caller.
	startMu sync.Mutex

	mu       sync.RWMutex
	progress Progress
	cancel   context.CancelFunc
	paused   bool
	pauseCh  chan struct{}
	// done is closed when the run goroutine exits. It is what makes stopping and restarting safe:
	// see the handover in Start.
	done chan struct{}

	subsMu sync.Mutex
	subs   map[chan Progress]bool

	// tracker measures how the mount actually tracked, from the frames this run writes. Optional:
	// nil when no plate solver is configured, and the session runs exactly as before.
	tracker *TrackMonitor

	// guider corrects the mount from those same frames. Also optional, and for a stronger reason than
	// the tracker: it MOVES HARDWARE. A session with no guider attached behaves exactly as it always
	// did, and every failure inside the guider is swallowed rather than losing the night's frames.
	guider *Guider
}

// SetGuider attaches self-guiding to this runner. Safe to call with nil.
func (r *Runner) SetGuider(g *Guider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.guider = g
}

// GuideStats reports self-guiding progress, or false when nothing is guiding.
func (r *Runner) GuideStats() (GuideStats, bool) {
	r.mu.RLock()
	g := r.guider
	r.mu.RUnlock()
	return g.Stats()
}

// currentGuider reads the attached guider under the lock.
func (r *Runner) currentGuider() *Guider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.guider
}

// TrackStats reports the tracking monitor's progress, or false when measurement is not running.
func (r *Runner) TrackStats() (TrackStats, bool) {
	r.mu.RLock()
	m := r.tracker
	r.mu.RUnlock()
	return m.Stats()
}

// SetTrackMonitor attaches tracking measurement to this runner. Safe to call with nil.
func (r *Runner) SetTrackMonitor(m *TrackMonitor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tracker = m
}

// NewRunner builds a sequencer over a device-server client.
func NewRunner(client *Client, recorder Recorder) *Runner {
	return &Runner{
		client:   client,
		recorder: recorder,
		progress: Progress{Status: StatusIdle},
		subs:     map[chan Progress]bool{},
	}
}

// Start validates and launches a session. It returns as soon as the session is accepted; progress
// arrives on the subscription channel.
func (r *Runner) Start(ctx context.Context, req Request) (Progress, error) {
	if err := req.Sequence.Validate(); err != nil {
		return Progress{}, err
	}
	if req.Root == "" {
		return Progress{}, fmt.Errorf("a capture root directory is required")
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()

	r.mu.Lock()
	if r.progress.Status == StatusRunning || r.progress.Status == StatusPaused {
		r.mu.Unlock()
		return r.Progress(), ErrSessionRunning
	}
	previous := r.done
	r.mu.Unlock()

	// Wait for the PREVIOUS run's goroutine to let go before taking over.
	//
	// Aborting only cancels its context: the loop still has to notice, abandon the frame in flight
	// and write its terminal row, and the last thing it does is publish that terminal status. Start
	// again inside that window — which is exactly what "stop, then resume the rest" does — and the
	// old run's "aborted" lands on top of the new one, so a session that is running perfectly well
	// reports itself stopped a second after it began. Cancellation makes this fast: the exposure
	// poll checks the context every 20 ms.
	if previous != nil {
		select {
		case <-previous:
		case <-time.After(runHandoverTimeout):
			return r.Progress(), fmt.Errorf(
				"the previous session is still stopping after %s; try again in a moment", runHandoverTimeout)
		}
	}

	plan := req.Sequence.order()
	id := int64(0)
	if r.recorder != nil {
		var err error
		if id, err = r.recorder.CreateSession(ctx, req, len(plan)); err != nil {
			return Progress{}, err
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.mu.Lock()
	r.cancel = cancel
	r.done = done
	r.paused = false
	r.pauseCh = make(chan struct{})
	r.progress = Progress{
		SessionID: id, Status: StatusRunning,
		TotalFrames: len(plan), StartedAt: time.Now(),
		Captured: map[string]int{},
	}
	started := r.progress
	r.mu.Unlock()
	r.publish()

	go func() {
		defer close(done)
		r.run(runCtx, req, plan)
	}()
	return started, nil
}

// Pause stops after the current exposure finishes — never mid-frame, which would waste it.
func (r *Runner) Pause() Progress {
	r.mu.Lock()
	if r.progress.Status == StatusRunning {
		r.paused = true
		r.progress.Status = StatusPaused
		r.progress.Message = "paused after the current frame"
	}
	r.mu.Unlock()
	r.publish()
	return r.Progress()
}

// Resume continues a paused session.
func (r *Runner) Resume() Progress {
	r.mu.Lock()
	if r.progress.Status == StatusPaused {
		r.paused = false
		r.progress.Status = StatusRunning
		r.progress.Message = ""
		if r.pauseCh != nil {
			close(r.pauseCh)
			r.pauseCh = make(chan struct{})
		}
	}
	r.mu.Unlock()
	r.publish()
	return r.Progress()
}

// Abort stops the session and the current exposure.
func (r *Runner) Abort() Progress {
	r.mu.Lock()
	cancel := r.cancel
	if r.progress.Status == StatusRunning || r.progress.Status == StatusPaused {
		r.progress.Status = StatusAborted
		r.progress.Message = "aborted"
	}
	r.paused = false
	id, snapshot := r.progress.SessionID, r.progress
	r.mu.Unlock()

	if cancel != nil {
		// A run is in flight. Cancelling it makes the loop reach finish, which writes the row.
		cancel()
		r.publish()
		return r.Progress()
	}
	// Nothing is running, so nothing else will ever write this row. That is the state a FAILED run
	// leaves behind, and it is why Stop appeared to do nothing at all: the loop had already exited,
	// so Abort changed a status nobody was going to persist and the session stayed "running". Close
	// it here instead. A never-started runner has no session id and is skipped.
	r.persistTerminal(context.Background(), id, snapshot)
	r.publish()
	return r.Progress()
}

// Progress is the current session state.
func (r *Runner) Progress() Progress {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p := r.progress
	if p.Captured != nil {
		cp := make(map[string]int, len(p.Captured))
		for k, v := range p.Captured {
			cp[k] = v
		}
		p.Captured = cp
	}
	return p
}

// Subscribe streams progress updates. The channel is buffered and lossy: a slow consumer misses
// intermediate updates rather than stalling a capture.
func (r *Runner) Subscribe() (<-chan Progress, func()) {
	ch := make(chan Progress, 8)
	r.subsMu.Lock()
	r.subs[ch] = true
	r.subsMu.Unlock()
	return ch, func() {
		r.subsMu.Lock()
		delete(r.subs, ch)
		r.subsMu.Unlock()
		close(ch)
	}
}

func (r *Runner) publish() {
	p := r.Progress()
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// run executes the planned exposures.
func (r *Runner) run(ctx context.Context, req Request, plan []Step) {
	planner := dither.NewPlanner(req.DitherRadiusPx)
	state := &runState{req: req, planner: planner}

	for i, step := range plan {
		if !r.waitWhilePaused(ctx) {
			r.finish(ctx, StatusAborted, nil)
			return
		}
		r.setStep(i, step)
		err := r.captureOne(ctx, state, step, i)
		if err == nil {
			// A frame landed, so whatever went wrong before is over.
			state.recoveries = 0
			continue
		}
		if ctx.Err() != nil {
			r.finish(ctx, StatusAborted, nil)
			return
		}
		// A cable nudged at 2am used to end the night. The driver reconnects on its own within
		// seconds, so a DEVICE error is now waited out and the plan carries on from the next
		// frame; anything else — a read-only output directory, a full disk — still fails at once,
		// because retrying those forever is worse than stopping. See recover.go.
		if !isRecoverable(err) {
			r.finish(ctx, StatusFailed, err)
			return
		}
		state.recoveries++
		if state.recoveries > maxConsecutiveRecoveries {
			// Rescued this many times with nothing to show for it, the rig is not hiccuping, it has
			// stopped working — and a session that keeps "recovering" until dawn is worse than one
			// that says so at midnight.
			r.finish(ctx, StatusFailed, fmt.Errorf(
				"gave up after %d hardware recoveries in a row with no frame in between: %w",
				maxConsecutiveRecoveries, err))
			return
		}
		if rerr := r.recoverFromDeviceError(ctx, err); rerr != nil {
			if ctx.Err() != nil {
				r.finish(ctx, StatusAborted, nil)
				return
			}
			r.finish(ctx, StatusFailed, rerr)
			return
		}
		// The frame that failed is not retried: the exposure is gone either way, and re-taking it
		// would double the time on this step for no gain. The plan simply continues.
	}
	r.finish(ctx, StatusCompleted, nil)
}

// waitWhilePaused blocks while the session is paused; false means the session was cancelled.
func (r *Runner) waitWhilePaused(ctx context.Context) bool {
	for {
		r.mu.RLock()
		paused, ch := r.paused, r.pauseCh
		r.mu.RUnlock()
		if !paused {
			return ctx.Err() == nil
		}
		select {
		case <-ctx.Done():
			return false
		case <-ch:
		case <-time.After(time.Second):
		}
	}
}

func (r *Runner) setStep(index int, step Step) {
	r.mu.Lock()
	r.progress.StepIndex = index
	r.progress.CurrentFilter = step.Filter
	r.progress.ExposureUs = step.ExposureUs
	r.mu.Unlock()
	r.publish()
}

// finish records the terminal state once.
func (r *Runner) finish(ctx context.Context, status Status, err error) {
	r.mu.Lock()
	// An explicit Abort has already set the status; do not overwrite it with "completed".
	if r.progress.Status != StatusAborted || status == StatusFailed {
		r.progress.Status = status
	}
	if err != nil {
		r.progress.Error = err.Error()
	}
	r.progress.ExposureEnds = time.Time{}
	id, snapshot := r.progress.SessionID, r.progress
	r.cancel = nil
	r.mu.Unlock()
	r.persistTerminal(ctx, id, snapshot)
	r.publish()
}

// persistTerminal writes the run's final state to the database.
//
// Split out of finish because Abort needs it too, and it REPORTS a failure rather than discarding
// it. This one write is the only thing that closes a session row, and when it silently did not
// happen the run stayed "running" in the logbook forever — a phantom night implying frames are still
// arriving, which the user can only clear by restarting the engine. There is no logger in this
// package by design, so the complaint goes where its other messages go: the progress the UI is
// already watching.
func (r *Runner) persistTerminal(ctx context.Context, id int64, snapshot Progress) {
	if r.recorder == nil || id == 0 {
		return
	}
	// A cancelled ctx must not stop the final state from being written.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.recorder.UpdateSession(saveCtx, id, snapshot.Status, snapshot); err != nil {
		r.note(fmt.Sprintf("session %d could not be closed in the database: %v", id, err))
	}
}
