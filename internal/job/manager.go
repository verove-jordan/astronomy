// Package job runs pipeline work in a background worker pool, persists each job's lifecycle to
// Postgres, and publishes live progress to subscribers (consumed by the API's SSE endpoint).
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/livestack"
	"github.com/verove-jordan/astronomy/internal/llm"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/s3conn"
	"github.com/verove-jordan/astronomy/internal/secret"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/source"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
	"github.com/verove-jordan/astronomy/internal/turns"
	"github.com/verove-jordan/astronomy/internal/videoout"
)

// Event is a progress update for a job, streamed to subscribers. A log line carries Line + Ts; a
// live resource reading carries RSSBytes/CPUPercent/PeakRSSBytes (with no Line).
type Event struct {
	JobID    int64  `json:"job_id"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Step     string `json:"step"`
	Line     string `json:"line,omitempty"`
	Ts       int64  `json:"ts,omitempty"`      // wall-clock ms the line was captured
	Preview  string `json:"preview,omitempty"` // a preview PNG path produced mid-run

	RSSBytes     int64   `json:"rss_bytes,omitempty"`      // live resident memory of the step's subprocess
	CPUPercent   float64 `json:"cpu_percent,omitempty"`    // live CPU usage (100 == one core)
	PeakRSSBytes int64   `json:"peak_rss_bytes,omitempty"` // peak resident memory seen this step

	// Live byte progress for an S3 transfer job (streamed, never persisted — the Progress int is).
	BytesDone   int64 `json:"bytes_done,omitempty"`
	BytesTotal  int64 `json:"bytes_total,omitempty"`
	BytesPerSec int64 `json:"bytes_per_sec,omitempty"` // smoothed transfer throughput (débit)

	// Iteration carries one supervised-finish pass (preview + tier + defects + scores) as it happens,
	// so the UI streams the AI agent's iterations live instead of only after the job finishes.
	Iteration    *postprocess.IterationRecord `json:"iteration,omitempty"`
	StagePreview *postprocess.StagePreview    `json:"stage_preview,omitempty"` // one saved processing-milestone preview, streamed live

	// StagePreviews carries the accumulated milestone previews in one shot — used only by the SSE
	// reconnect snapshot so a page reloaded mid-run re-hydrates the intermediary-image timeline.
	StagePreviews []postprocess.StagePreview `json:"stage_previews,omitempty"`

	Done bool `json:"done,omitempty"`
}

// stepPercent maps a 1-based step Index of Total to a progress percentage that treats the *running*
// step as half-complete: the bar advances when a step begins and again when the next one does, and a
// running step never reaches 100%. The final "aligning channels + combining" step is Index==Total —
// reporting Index*100/Total jumped the bar to 100% the instant that long step began, so it looked
// finished while still working. 100% is reserved for the job's "done" event (after which the UI swaps
// the progress card for the result panels). Total<=0 (e.g. planetary, which has no step count) → 0.
func stepPercent(index, total int) int {
	if total <= 0 {
		return 0
	}
	pct := (index*2 - 1) * 100 / (total * 2)
	if pct > 99 {
		pct = 99
	}
	return pct
}

// Log persistence tuning: keep a generous tail so a long step survives a browser refresh, and flush
// it on a debounce (every flushEveryN lines or flushInterval, whichever first) to bound DB writes.
const (
	logCap      = 5000
	flushEveryN = 15
)

var flushInterval = time.Second

// logEntry is one captured log line with the wall-clock time it arrived.
type logEntry struct {
	ts   int64
	line string
}

// encodeLog renders the log tail as newline-delimited "<ms>|<line>" rows for the jobs.log_tail
// column; the frontend splits each row at the first '|' to recover the timestamp.
func encodeLog(entries []logEntry) string {
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strconv.FormatInt(e.ts, 10))
		b.WriteByte('|')
		b.WriteString(e.line)
	}
	return b.String()
}

// Manager owns the worker pool and the subscriber registry.
// TurnHub is the subset of *turns.Sessions the manager needs to surface a supervised finish as a live,
// steerable conversation: mint a turn, stream per-iteration bubbles, block for the expensive-step
// confirmation, drain free-text nudges, and end the turn. nil (unset) → conversations off, and every
// supervised run behaves exactly as before.
type TurnHub interface {
	Start() string
	Publish(turnID string, e turns.Event)
	Await(ctx context.Context, turnID, callID string) (approve bool, choice string, ok bool)
	Finish(turnID string)
	DrainMessages(turnID string) (texts []string, stop bool)
}

type Manager struct {
	store  *store.Store
	runner *siril.Runner
	cfg    *config.Config
	s3conn *s3conn.Service // resolves the default S3 connection (UI-managed) for pipeline transfers/backups
	turns  TurnHub         // shared turn transport for supervised-job conversations; nil → conversations off

	queue     chan int64
	seqQueue  chan int64 // sequential lane: stacked "Add to queue" jobs run one-at-a-time, auto-advancing
	xferQueue chan int64 // S3 transfer lane: uploads/downloads run in their own pool, never starving runs
	mu        sync.Mutex
	subs      map[int64][]chan Event
	cancels   map[int64]context.CancelFunc // cancel funcs for in-flight jobs (kill support)
	pauses    map[int64]*pauseGate         // cooperative pause signals for in-flight jobs (guarded by mu)
	jobTurns  map[int64]string             // supervised jobs → their conversation turn id (guarded by mu)

	// Live-preview retention for reconnecting clients: the latest preview PNG path and the accumulated
	// milestone previews per running job. SSE events are dropped when no subscriber is attached, so this
	// lets a page reloaded mid-run re-hydrate its preview + intermediary-image timeline from the snapshot.
	// Cleared when the job finishes. Guarded by mu.
	lastPreview   map[int64]string
	stagePreviews map[int64][]postprocess.StagePreview

	confirmSeq int64 // monotonic counter for unique expensive-step confirmation call ids

	locker *pathLocker // serializes jobs whose input roots overlap (shared library/output)
}

// RunRequest is the user-supplied job configuration (also persisted as the job's params JSON).
type RunRequest struct {
	Path string `json:"path"`
	// Paths, when non-empty, are the multiple capture folders to merge into one session (cross-folder
	// multi-select). Path stays the primary dir (first selected) used for the session, target lock, and
	// run naming. Empty Paths → single-folder run over Path (unchanged).
	Paths               []string          `json:"paths,omitempty"`
	Mode                string            `json:"mode"`
	Format              string            `json:"format"`
	FilterMap           map[string]string `json:"filter_map,omitempty"`            // detected/known filter → chosen channel ("ignore" drops)
	DropWheelTransition *bool             `json:"drop_wheel_transition,omitempty"` // override preset default
	ColorCalibration    *bool             `json:"color_calibration,omitempty"`     // override preset default
	Denoise             *bool             `json:"denoise,omitempty"`               // false disables denoise
	HaExcludeStars      *bool             `json:"ha_exclude_stars,omitempty"`      // true: screen Ha onto nebulosity only, not stars

	// Nightscape (milkyway) overrides. Look selects the render style (natural/iphone/deepsky);
	// ForegroundFrame overrides the auto-picked clean foreground (a raw frame path); Orientation
	// sets the final display transform (auto|none|cw|ccw|180, optionally +"-flip"). Brightness sets
	// the auto-levels sky-background target (darker|balanced|brighter, or a 0..0.5 number).
	// Dark/Flat/BiasDir are optional calibration-frame folders applied before stacking.
	Look            string `json:"look,omitempty"`
	ForegroundFrame string `json:"foreground_frame,omitempty"`
	Orientation     string `json:"orientation,omitempty"`
	// Palette selects the deep-sky channel→RGB colour mapping (natural|hargb|hoo|sho|hos|foraxx|mono);
	// empty → natural. A palette missing its required filters falls back (see pipeline.resolvePalette).
	Palette    string `json:"palette,omitempty"`
	Brightness string `json:"brightness,omitempty"`
	DarkDir    string `json:"dark_dir,omitempty"`
	FlatDir    string `json:"flat_dir,omitempty"`
	BiasDir    string `json:"bias_dir,omitempty"`

	// Comet (mode "comet") optional manual comet position override: the comet's pixel coordinates in the
	// first (X1,Y1) and last (X2,Y2) star-aligned frame. All four > 0 → override; otherwise auto-detect.
	CometX1 float64 `json:"comet_x1,omitempty"`
	CometY1 float64 `json:"comet_y1,omitempty"`
	CometX2 float64 `json:"comet_x2,omitempty"`
	CometY2 float64 `json:"comet_y2,omitempty"`

	// Reuse controls cross-session light reuse. ReuseDisabled turns it off for this run; ReuseSessions
	// (when non-empty) restricts the folded-in prior data to the listed session ids (the user's
	// selection from the auto-discovered preview). Empty + enabled → fold in all matching sessions.
	ReuseDisabled bool    `json:"reuse_disabled,omitempty"`
	ReuseSessions []int64 `json:"reuse_sessions,omitempty"`

	// Live configures a live-stacking session (mode "livestack"). Path is still the lock/display key
	// (a real directory for a local source, or a synthetic "s3://bucket/prefix" for an S3 source).
	Live *LiveRequest `json:"live,omitempty"`

	// Supervise opts this run into the local-AI-agent finish (auto-tune the GIMP composite with a host
	// vision model). Default false → the standard single-pass finish. Requires ASTRO_LLM_URL reachable.
	Supervise bool `json:"supervise,omitempty"`

	// Params is an optional JSON object of fine tunable-knob overrides — the same whitelist and clamps
	// the in-run supervisor uses (pipeline.ApplyParamPatch), so the AI agent (or an advanced user) can
	// START a run with chosen parameters, not only coarse mode toggles. Validated at Enqueue (a
	// malformed body is rejected before any work); unknown keys are ignored with a job log line.
	Params json.RawMessage `json:"params,omitempty"`
	// Tier caps the supervised loop's re-entry ceiling ("A"|"B"|"C"; "" → mode default) and MaxIters
	// caps its iterations (0 → default). Goal is a free-text objective the agent carries for the run
	// (recorded on the job; folded into the supervisor's first critique as user guidance).
	Tier     string `json:"tier,omitempty"`
	MaxIters int    `json:"max_iters,omitempty"`
	Goal     string `json:"goal,omitempty"`
	// SeriesID links this job to an agent improvement series (0 = none). See internal/store series.
	SeriesID int64 `json:"series_id,omitempty"`

	// Refine, when set, makes this job re-finish an already-completed run (no re-stack) under the AI
	// supervisor instead of processing from scratch. Built by Manager.Refine from a source job; the
	// worker branches to the finish-only path (see execute → executeRefine).
	Refine *RefineRequest `json:"refine,omitempty"`

	// Rerun, when set, re-runs an already-completed run from the stage a parameter edit requires
	// (pipeline.RerunFromStage), in place — the manual, non-supervised counterpart of Refine. Built by
	// Manager.Rerun from a source job; the worker branches to executeRerun.
	Rerun *RerunRequest `json:"rerun,omitempty"`

	// DenoiseFinal, when set, runs GraXpert AI denoise on an already-completed run's final image on demand
	// (offloaded to the native host GraXpert service when ASTRO_GRAXPERT_URL is set). Built by
	// Manager.DenoiseFinal; the worker branches to executeDenoiseFinal.
	DenoiseFinal *DenoiseFinalRequest `json:"denoise_final,omitempty"`

	// Sequential routes this job into the single-worker queue lane so stacked "Add to queue" jobs run
	// one-at-a-time in submission order, auto-advancing — instead of the parallel pool. Default false.
	Sequential bool `json:"sequential,omitempty"`

	// CalibExclude lists calib.SuggestID keys (per light-set, per role) the user unchecked in the Import
	// "Calibration" panel; those darks/flats/bias are dropped from each channel's matched selection.
	CalibExclude []string `json:"calib_exclude,omitempty"`

	// CalibPlan is a frozen snapshot of the calibration masters matched for this capture at queue time
	// (the Import "Calibration" preview), so the job page can show which darks/flats/bias are included and
	// with what params — visible while queued/running, before the run's result masters exist. Purely
	// informational: the pipeline re-matches independently at run time (honoring CalibExclude). nil for
	// CLI/non-UI jobs, which simply show no calibration panel.
	CalibPlan *calib.CalibPreview `json:"calib_plan,omitempty"`

	// Transfer, when set, makes this an S3 transfer job (upload/sync/download/remove-local) instead of a
	// pipeline run — it is intercepted before mode parsing and reuses the whole job progress/SSE stack.
	Transfer *TransferRequest `json:"transfer,omitempty"`

	// StorageMode selects where a pipeline run's files live: "local" (default, keep) or "s3" (pull inputs,
	// process, push inputs+outputs, then remove local copies). S3 targets the run's S3Target.
	StorageMode string    `json:"storage_mode,omitempty"`
	S3          *S3Target `json:"s3,omitempty"`
	// LowDisk overrides the server ASTRO_S3_LOW_DISK default for this full-S3 deep-sky/nebula run: when on,
	// inputs are staged from S3 one frame-type/channel wave at a time and freed after, so peak local disk
	// stays ≈ one channel's frames. nil → the server default. See internal/job/stager.go.
	LowDisk *bool `json:"low_disk,omitempty"`

	// Backup / Restore, when set, make this a backup-everything (or restore) job instead of a pipeline run
	// — intercepted before mode parsing like Transfer, reusing the whole job progress/SSE stack.
	Backup  *BackupRequest  `json:"backup,omitempty"`
	Restore *RestoreRequest `json:"restore,omitempty"`
}

// BackupRequest snapshots precious local state (Postgres db, calibration library, LP atlas, browser app
// state) to <Prefix>/backup/<stamp>/ in Bucket. Credentials are env-only (never carried here). AppState is
// the UI-exported browser JSON (favorites/setups/prefs + AI chats) — only the frontend can produce it.
// StampMs is set server-side.
type BackupRequest struct {
	Bucket     string   `json:"bucket"`
	Prefix     string   `json:"prefix"`
	Components []string `json:"components"` // db | library | atlas | appstate
	AppState   string   `json:"appstate,omitempty"`
	StampMs    int64    `json:"stamp_ms"`
}

// RestoreRequest restores the chosen components from <Prefix>/backup/<Stamp>/ in Bucket. The appstate
// component is applied browser-side (fetched via GET /api/backup/appstate), not by this job.
type RestoreRequest struct {
	Bucket     string   `json:"bucket"`
	Prefix     string   `json:"prefix"`
	Stamp      string   `json:"stamp"`
	Components []string `json:"components"`
}

// TransferRequest describes an S3 folder transfer. Credentials are NOT carried here (env only); Bucket +
// Prefix + Namespace ("data" for captures / "output" for results) + RelPath locate the folder and its
// mirror key (`<Prefix>/<Namespace>/<RelPath>`).
type TransferRequest struct {
	Op        string `json:"op"` // upload | sync | download | removeLocal
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	Namespace string `json:"namespace"` // "data" | "output"
	RelPath   string `json:"rel_path"`
	// LocalRoot, when set, is an ABSOLUTE local root OUTSIDE DataDir/OutputDir (an external drive) that the
	// transfer walks instead of the namespace root. The API validates it against the browse allowlist before
	// enqueuing (localfs.Allowed) — it is never derived from an untrusted body alone. With it, keys are
	// `<Prefix>/<RelPath>/…` (Namespace stays empty) and the classified data-plan is skipped: these are
	// arbitrary files, not AstroStack captures.
	LocalRoot string `json:"local_root,omitempty"`
	// Verify upgrades a sync to content-verified — upload only files MISSING or CORRUPTED, not just
	// size-changed (see transfer.Request.Verify). Used by the external-drive copy so a half-written mirror
	// object is re-uploaded rather than trusted.
	Verify bool `json:"verify,omitempty"`
	// ExcludeDirs names subdirectories the transfer walk skips entirely (see transfer.Request.ExcludeDirs).
	// The calibration-library mirror sets it to ["catalogues"] so the multi-GB Gaia catalogues tree under
	// LibraryDir is never uploaded.
	ExcludeDirs []string `json:"exclude_dirs,omitempty"`
}

// S3Target is the bucket + prefix a full-S3 run reads inputs from and pushes results to.
type S3Target struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

// LiveRequest is the live-stacking source + capture settings. Credentials are never carried here — the
// S3 access keys come from the host environment (config). SourceKind "local" watches Path; "s3" watches
// Bucket/Prefix.
type LiveRequest struct {
	SourceKind  string  `json:"source_kind,omitempty"` // "local" (default) | "s3"
	Bucket      string  `json:"bucket,omitempty"`
	Prefix      string  `json:"prefix,omitempty"`
	ExposureSec float64 `json:"exposure_sec,omitempty"` // per-sub exposure (fallback + integration display)
}

// RefineRequest re-finishes an existing completed run under the AI supervisor. RunDir is the run's
// output folder (output/<object>/<runID>); the finish is re-run from its on-disk masters (Tier A/B) or,
// when AllowRestack is set and the raw frames are still present, re-stacked from scratch (Tier C).
type RefineRequest struct {
	RunDir       string `json:"run_dir"`
	MaxIters     int    `json:"max_iters,omitempty"`     // 0 → engine default (4, hard max 8)
	Tier         string `json:"tier,omitempty"`          // ceiling "A"|"B"|"C"; "" → full (C when raws available)
	AllowRestack bool   `json:"allow_restack,omitempty"` // permit Tier-C re-stack from the source input dir
	// Params are fine knob overrides seeded onto the preset BEFORE the supervised loop (the same
	// whitelist/clamps as RunRequest.Params) — "retry this run with these parameters".
	Params json.RawMessage `json:"params,omitempty"`
}

// RerunRequest re-runs an already-completed deepsky/nebula run from the stage a parameter edit requires,
// in place (overwriting the run's files) — the manual, non-supervised counterpart of RefineRequest.
// RunDir is the run's output folder; Stage is the timeline stage the user restarted from (the re-entry
// floor); Params are the knob overrides applied onto the run's checkpoint baseline (the same whitelist
// and clamps as RunRequest.Params). See pipeline.RerunFromStage.
type RerunRequest struct {
	RunDir string          `json:"run_dir"`
	Stage  string          `json:"stage,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// DenoiseFinalRequest denoises an already-completed run's final image with GraXpert on demand (see
// Manager.DenoiseFinal / executeDenoiseFinal).
type DenoiseFinalRequest struct {
	RunDir string `json:"run_dir"`
}

// inputRoots returns the capture folders this run scans: the multi-select Paths when set, else just
// Path. The first element is the primary dir (session, target lock, run naming).
func (r RunRequest) inputRoots() []string {
	if len(r.Paths) > 0 {
		return r.Paths
	}
	return []string{r.Path}
}

// reuseSessions converts the request's session allow-list to the set the planner expects: nil means
// "all discovered sessions", a populated set restricts to the chosen ids.
func (r RunRequest) reuseSessions() map[int64]bool {
	if len(r.ReuseSessions) == 0 {
		return nil
	}
	set := make(map[int64]bool, len(r.ReuseSessions))
	for _, id := range r.ReuseSessions {
		set[id] = true
	}
	return set
}

// NewManager creates a Manager with a bounded queue. hub is the shared turn transport (also handed to
// the API layer) used to surface supervised jobs as conversations; nil disables that (jobs run headless).
func NewManager(st *store.Store, runner *siril.Runner, cfg *config.Config, hub TurnHub) *Manager {
	return &Manager{
		store:         st,
		runner:        runner,
		cfg:           cfg,
		turns:         hub,
		s3conn:        newS3ConnService(st, cfg),
		queue:         make(chan int64, 256),
		seqQueue:      make(chan int64, 256),
		xferQueue:     make(chan int64, 256),
		subs:          map[int64][]chan Event{},
		cancels:       map[int64]context.CancelFunc{},
		pauses:        map[int64]*pauseGate{},
		jobTurns:      map[int64]string{},
		lastPreview:   map[int64]string{},
		stagePreviews: map[int64][]postprocess.StagePreview{},
		locker:        newPathLocker(),
	}
}

// newS3ConnService builds the encrypted-connection service for the worker (nil when the master key can't be
// resolved — pipeline S3 then falls back to the env credentials).
func newS3ConnService(st *store.Store, cfg *config.Config) *s3conn.Service {
	box, err := secret.NewBox(cfg.EncryptionKey, cfg.SecretKeyFile)
	if err != nil {
		return nil
	}
	return s3conn.New(st, box)
}

// lockTarget serializes jobs that share an input directory so a new run cannot race a still-running one
// on the shared calibration library and output directory. Jobs over different inputs run concurrently.
// It blocks until the lock is free and returns the unlock function.
// lockTarget serializes this run against any other job whose input roots overlap (equal or nested
// paths). Kept as a named seam; the real logic lives in pathLocker.
func (m *Manager) lockTarget(paths ...string) func() {
	return m.locker.Acquire(paths)
}

// Start launches n parallel worker goroutines (draining the main queue) plus one dedicated worker for
// the sequential lane, all stopping when ctx is cancelled. The single seq worker is what makes stacked
// "Add to queue" jobs run strictly one-at-a-time in submission order, auto-advancing.
func (m *Manager) Start(ctx context.Context, n int) {
	// Reconcile jobs orphaned by a previous server instance. The worker pool is in-process and does not
	// survive a restart (notably an `air` hot-reload rebuild), so any job still "running" in the DB at
	// boot has no live worker — mark it failed rather than leave it hanging at its last percentage.
	if rows, err := m.store.FailRunningJobs(ctx, "interrupted by a server restart"); err != nil {
		log.Printf("astrostack: reconcile running jobs failed: %v", err)
	} else if rows > 0 {
		log.Printf("astrostack: reconciled %d orphaned running job(s) as failed on startup", rows)
	}
	for i := 0; i < n; i++ {
		go m.worker(ctx, m.queue)
	}
	go m.worker(ctx, m.seqQueue)
	// Two transfer workers so a couple of uploads/downloads can overlap without touching the run pool.
	go m.worker(ctx, m.xferQueue)
	go m.worker(ctx, m.xferQueue)
	// Re-dispatch jobs a previous instance left 'queued'. Enqueue schedules a job by pushing its id onto
	// an in-process lane channel, which is lost on restart — so, like the orphaned-running reconcile above,
	// a job queued when the previous instance stopped has no live worker and would sit queued forever.
	m.redispatchQueued(ctx)
	// Auto-resume error-paused jobs (transient S3 failures) with backoff — manual pauses are left alone.
	go m.autoResumePaused(ctx)
}

// redispatchQueued re-schedules every job the DB still has as 'queued' by pushing its id back onto the
// right lane, oldest-first. Scheduling is an in-process channel push (see Enqueue/laneFor) that does not
// survive a restart, so a job queued when the previous instance stopped (e.g. an `air` rebuild) would
// otherwise stay 'queued' with no worker to run it. Called once from Start, after the workers are up.
func (m *Manager) redispatchQueued(ctx context.Context) {
	jobs, err := m.store.ListQueuedJobs(ctx)
	if err != nil {
		log.Printf("astrostack: reload queued jobs failed: %v", err)
		return
	}
	dispatched := 0
	for _, j := range jobs {
		var req RunRequest
		if err := json.Unmarshal(j.Params, &req); err != nil {
			log.Printf("astrostack: re-dispatch job %d: invalid params: %v", j.ID, err)
			continue
		}
		select {
		case m.laneFor(req) <- j.ID:
			dispatched++
		default:
			log.Printf("astrostack: re-dispatch job %d: lane full, left queued", j.ID)
		}
	}
	if dispatched > 0 {
		log.Printf("astrostack: re-dispatched %d queued job(s) on startup", dispatched)
	}
}

// Enqueue creates a session and a queued job (kind = mode), then schedules it. Returns the job id.
func (m *Manager) Enqueue(ctx context.Context, req RunRequest) (int64, error) {
	// Validate fine-knob overrides up front on a scratch preset — a malformed params body must fail
	// the REQUEST, not the worker mid-run. execute() re-applies onto the real preset and logs it.
	if len(req.Params) > 0 && req.Transfer == nil && req.Backup == nil && req.Restore == nil {
		if mo, merr := mode.ParseMode(req.Mode); merr == nil {
			scratch := mode.For(mo)
			if _, perr := pipeline.ApplyParamPatch(&scratch, req.Params); perr != nil {
				return 0, fmt.Errorf("invalid params: %w", perr)
			}
		}
	}
	sessionID, err := m.store.CreateSession(ctx, req.Path, "")
	if err != nil {
		return 0, err
	}
	p, _ := json.Marshal(req)
	id, err := m.store.CreateJob(ctx, sessionID, req.Mode, p)
	if err != nil {
		return 0, err
	}
	m.linkSeries(ctx, id, req.SeriesID)
	// Supervised (and refine) jobs surface as a live conversation: mint the turn now, before the job can
	// be dequeued, so the worker always finds it (and the API can hand the turn id straight back). Look it
	// up with TurnFor. Non-supervised jobs — and headless mode (m.turns nil) — get none.
	if m.turns != nil && (req.Supervise || req.Refine != nil) {
		turnID := m.turns.Start()
		m.mu.Lock()
		m.jobTurns[id] = turnID
		m.mu.Unlock()
	}
	select {
	case m.laneFor(req) <- id:
	default:
		return 0, fmt.Errorf("job queue is full")
	}
	return id, nil
}

// Restart re-runs a finished job as a brand-new job with the same parameters, returning the new job id.
// The original job's stored params (its RunRequest) are replayed verbatim through Enqueue, so it lands
// in the same lane (parallel or sequential) and re-applies the same folders, mode, calibration and reuse
// choices. The original record is left intact for history. It refuses a job that is still queued or
// running — there is nothing to restart while it is live.
func (m *Manager) Restart(ctx context.Context, id int64) (int64, error) {
	j, err := m.store.GetJob(ctx, id)
	if err != nil {
		return 0, err
	}
	if j.Status == store.JobQueued || j.Status == store.JobRunning {
		return 0, fmt.Errorf("job %d is still %s", id, j.Status)
	}
	if j.Status == store.JobPaused {
		return 0, fmt.Errorf("job %d is paused — continue it instead of restarting", id)
	}
	var req RunRequest
	if err := json.Unmarshal(j.Params, &req); err != nil {
		return 0, fmt.Errorf("job %d has invalid params: %w", id, err)
	}
	return m.Enqueue(ctx, req)
}

// RetryTuned re-runs a finished job from scratch (full re-process, including the stack) with tuned
// fine parameters and an optional goal, under the AI supervisor. The new run warm-starts from the
// target's best prior iteration (cross-run memory), so it CONTINUES the improvement trajectory —
// this is the agent's structural-change lever for every mode (frame selection, grading, deconvolution).
func (m *Manager) RetryTuned(ctx context.Context, id int64, params json.RawMessage, goal string, maxIters int) (int64, error) {
	j, err := m.store.GetJob(ctx, id)
	if err != nil {
		return 0, err
	}
	if j.Status == store.JobQueued || j.Status == store.JobRunning {
		return 0, fmt.Errorf("job %d is still %s", id, j.Status)
	}
	var req RunRequest
	if err := json.Unmarshal(j.Params, &req); err != nil {
		return 0, fmt.Errorf("job %d has invalid params: %w", id, err)
	}
	req.Refine, req.Live = nil, nil
	req.Sequential = false
	req.Supervise = true
	req.Params = params
	req.Goal = goal
	req.MaxIters = maxIters
	// A goal-driven retry with no series opens one: the durable improvement campaign the UI shows as
	// an attempt timeline and the auto-continue policy operates on.
	if req.SeriesID == 0 && goal != "" {
		var res struct {
			Object string `json:"object"`
		}
		_ = json.Unmarshal(j.Result, &res)
		if sid, serr := m.CreateSeries(ctx, store.AgentSeries{
			Object: res.Object, Kind: req.Mode, InputPath: req.Path, Goal: goal,
		}); serr == nil {
			req.SeriesID = sid
		}
	}
	return m.Enqueue(ctx, req)
}

// Refine enqueues a job that re-finishes an already-completed run under the AI supervisor without
// re-stacking (Tier A/B), or — when opts.AllowRestack is set and the raw frames survive — re-stacking
// too (Tier C). It clones the source job's processing choices (mode/format/filter map/calibration) so
// the finish matches, then lands in the normal worker to lock the target, stream progress and persist
// iterations like any run. opts.RunDir defaults to the source run's output dir. Returns the new job id.
func (m *Manager) Refine(ctx context.Context, sourceJobID int64, opts RefineRequest) (int64, error) {
	j, err := m.store.GetJob(ctx, sourceJobID)
	if err != nil {
		return 0, err
	}
	if j.Status == store.JobQueued || j.Status == store.JobRunning {
		return 0, fmt.Errorf("job %d is still %s", sourceJobID, j.Status)
	}
	var src RunRequest
	if err := json.Unmarshal(j.Params, &src); err != nil {
		return 0, fmt.Errorf("job %d has invalid params: %w", sourceJobID, err)
	}
	if opts.RunDir == "" {
		if opts.RunDir = runDirFromResult(j.Result); opts.RunDir == "" {
			return 0, fmt.Errorf("job %d has no completed run to refine", sourceJobID)
		}
	}
	req := src // clone the source's processing choices; only the refine-specific bits change
	req.Live = nil
	req.Sequential = false
	req.Supervise = true
	req.Refine = &opts
	return m.Enqueue(ctx, req)
}

// Rerun enqueues a job that re-runs an already-completed deepsky/nebula run from the stage a parameter
// edit requires, overwriting the run in place — the manual, non-supervised counterpart of Refine. It
// clones the source job's processing choices, so a Tier-C re-entry can re-stack from the raw frames and
// reuse the calibration library; the overrides are applied onto the run's checkpoint baseline at the
// cheapest re-entry tier. stage is the timeline stage the user restarted from ("" → the cheapest tier
// that fits the change). Returns the new job id (a fresh row per rerun for the params trail; the run
// dir is reused).
func (m *Manager) Rerun(ctx context.Context, sourceJobID int64, stage string, params json.RawMessage) (int64, error) {
	j, err := m.store.GetJob(ctx, sourceJobID)
	if err != nil {
		return 0, err
	}
	if j.Status == store.JobQueued || j.Status == store.JobRunning {
		return 0, fmt.Errorf("job %d is still %s", sourceJobID, j.Status)
	}
	var src RunRequest
	if err := json.Unmarshal(j.Params, &src); err != nil {
		return 0, fmt.Errorf("job %d has invalid params: %w", sourceJobID, err)
	}
	runDir := runDirFromResult(j.Result)
	if runDir == "" {
		return 0, fmt.Errorf("job %d has no completed run to rerun", sourceJobID)
	}
	// Validate the overrides up front against the source mode's preset — a malformed body fails the
	// REQUEST, not the worker mid-run (mirrors Enqueue).
	if len(params) > 0 {
		if mo, merr := mode.ParseMode(src.Mode); merr == nil {
			scratch := mode.For(mo)
			if _, perr := pipeline.ApplyParamPatch(&scratch, params); perr != nil {
				return 0, fmt.Errorf("invalid params: %w", perr)
			}
		}
	}
	// Clone the source's processing choices. Keep src.Params as the job's Params so execute() rebuilds the
	// ORIGINAL baseline preset (the fallback when a run predates the stage checkpoint); the NEW override
	// rides in Rerun.Params, which RerunFromStage applies onto the checkpoint to pick the re-entry tier.
	req := src
	req.Live, req.Refine, req.Transfer, req.Backup, req.Restore = nil, nil, nil, nil, nil
	req.Sequential = false
	req.Supervise = false
	req.Rerun = &RerunRequest{RunDir: runDir, Stage: stage, Params: params}
	return m.Enqueue(ctx, req)
}

// DenoiseFinal enqueues an on-demand job that runs GraXpert AI denoise on a completed run's final image.
// It clones the source job's mode/path for context and points at the run dir; when ASTRO_GRAXPERT_URL is
// set the worker offloads the denoise to the native host GraXpert service (faster + non-blocking).
// Returns the new job id. Refuses a job with no completed result.
func (m *Manager) DenoiseFinal(ctx context.Context, sourceJobID int64) (int64, error) {
	j, err := m.store.GetJob(ctx, sourceJobID)
	if err != nil {
		return 0, err
	}
	if j.Status != store.JobSucceeded {
		return 0, fmt.Errorf("job %d has no completed result to denoise", sourceJobID)
	}
	runDir := runDirFromResult(j.Result)
	if runDir == "" {
		return 0, fmt.Errorf("job %d has no run directory to denoise", sourceJobID)
	}
	var src RunRequest
	if err := json.Unmarshal(j.Params, &src); err != nil {
		return 0, fmt.Errorf("job %d has invalid params: %w", sourceJobID, err)
	}
	req := src
	req.Live, req.Refine, req.Transfer, req.Backup, req.Restore, req.Rerun = nil, nil, nil, nil, nil, nil
	req.Sequential, req.Supervise = false, false
	req.DenoiseFinal = &DenoiseFinalRequest{RunDir: runDir}
	return m.Enqueue(ctx, req)
}

// FreeLocal frees the local input + output files of a finished full-S3 run by enqueuing verified removeLocal
// transfers (each aborts unless every file is already on S3, so data is never lost). Returns the enqueued
// transfer job ids. Errors when the job is not a succeeded full-S3 run or has nothing local to free. This is
// the explicit counterpart to the auto-free that used to run after every full-S3 run.
func (m *Manager) FreeLocal(ctx context.Context, sourceJobID int64) ([]int64, error) {
	j, err := m.store.GetJob(ctx, sourceJobID)
	if err != nil {
		return nil, err
	}
	if j.Status != store.JobSucceeded {
		return nil, fmt.Errorf("job %d is not a completed run", sourceJobID)
	}
	var p RunRequest
	if err := json.Unmarshal(j.Params, &p); err != nil {
		return nil, fmt.Errorf("job %d has invalid params: %w", sourceJobID, err)
	}
	if !p.wantsS3Storage() {
		return nil, fmt.Errorf("job %d is not a full-S3 run", sourceJobID)
	}
	inputs, outRel := m.s3RunTargets(p, json.RawMessage(j.Result))

	var ids []int64
	enqueue := func(namespace, rel string) error {
		newID, err := m.Enqueue(ctx, RunRequest{Mode: "transfer", Transfer: &TransferRequest{
			Op: "removeLocal", Bucket: p.S3.Bucket, Prefix: p.S3.Prefix, Namespace: namespace, RelPath: rel,
		}})
		if err != nil {
			return err
		}
		ids = append(ids, newID)
		return nil
	}
	for _, rel := range inputs {
		if err := enqueue("data", rel); err != nil {
			return ids, err
		}
	}
	if outRel != "" {
		if err := enqueue("output", outRel); err != nil {
			return ids, err
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("job %d has no local files to free", sourceJobID)
	}
	return ids, nil
}

// runDirFromResult resolves a finished job's on-disk run directory from its stored result JSON. Deep-sky,
// comet and milkyway persist a pipeline.Result with output_dir; planetary persists a flat planetary.Result
// with out_base (<runDir>/<object>_stack) and no output_dir, so fall back to the directory of out_base.
// Empty when neither is present (nothing on disk to refine).
func runDirFromResult(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var r struct {
		OutputDir string `json:"output_dir"`
		OutBase   string `json:"out_base"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if r.OutputDir != "" {
		return r.OutputDir
	}
	if r.OutBase != "" {
		return filepath.Dir(r.OutBase)
	}
	return ""
}

// Cancel kills an in-flight job (cancelling its context terminates the running siril-cli). Returns
// false if the job is not currently running.
func (m *Manager) Cancel(id int64) bool {
	m.mu.Lock()
	cancel, ok := m.cancels[id]
	m.mu.Unlock()
	if ok {
		cancel() // running → terminate the siril-cli subprocess
		return true
	}
	// Not owned by this process. Either it is still queued (cancel-before-start), or it is "running" in
	// the DB but orphaned — its worker died with a previous server instance (an `air` restart) and will
	// never finish. Terminate both in the DB so the UI stops showing a phantom in-progress job.
	job, err := m.store.GetJob(context.Background(), id)
	if err != nil {
		return false
	}
	switch job.Status {
	case store.JobQueued, store.JobPaused:
		// Queued (not started) or paused (parked, no live worker) → mark cancelled directly.
		if err := m.store.FinishJob(context.Background(), id, store.JobCancelled, nil, "cancelled"); err != nil {
			return false
		}
		m.publish(Event{JobID: id, Status: store.JobCancelled, Step: "cancelled", Done: true})
		return true
	case store.JobRunning:
		if err := m.store.FinishJob(context.Background(), id, store.JobFailed, nil, "interrupted (server restarted while running)"); err != nil {
			return false
		}
		m.publish(Event{JobID: id, Status: store.JobFailed, Step: "interrupted", Done: true})
		return true
	default:
		return false
	}
}

// Subscribe returns a channel of events for a job and an unsubscribe function.
func (m *Manager) Subscribe(jobID int64) (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.mu.Lock()
	m.subs[jobID] = append(m.subs[jobID], ch)
	m.mu.Unlock()
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		subs := m.subs[jobID]
		for i, c := range subs {
			if c == ch {
				m.subs[jobID] = append(subs[:i], subs[i+1:]...)
				close(ch)
				break
			}
		}
	}
}

func (m *Manager) publish(e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retainPreview(e)
	for _, ch := range m.subs[e.JobID] {
		select {
		case ch <- e:
		default: // drop if the subscriber is slow; never block the worker
		}
	}
}

// retainPreview keeps the latest preview + the accumulated milestone previews for a running job, so a
// client that reconnects (page reload) re-hydrates them from the SSE snapshot instead of waiting for the
// next live event. Cleared when the job finishes. Caller holds m.mu.
func (m *Manager) retainPreview(e Event) {
	if e.Done {
		delete(m.lastPreview, e.JobID)
		delete(m.stagePreviews, e.JobID)
		return
	}
	if e.Preview != "" {
		m.lastPreview[e.JobID] = e.Preview
	}
	if e.StagePreview == nil {
		return
	}
	list := m.stagePreviews[e.JobID]
	for i := range list { // upsert by index so a re-emitted milestone updates in place
		if list[i].Index == e.StagePreview.Index {
			list[i] = *e.StagePreview
			return
		}
	}
	m.stagePreviews[e.JobID] = append(list, *e.StagePreview)
}

// PreviewSnapshot returns the latest preview path and a copy of the accumulated milestone previews for a
// job — used by the SSE reconnect snapshot so a page reloaded mid-run restores its live preview and
// intermediary-image timeline. Empty for a job that has produced no preview yet.
func (m *Manager) PreviewSnapshot(jobID int64) (string, []postprocess.StagePreview) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stages := m.stagePreviews[jobID]
	if len(stages) == 0 {
		return m.lastPreview[jobID], nil
	}
	out := make([]postprocess.StagePreview, len(stages))
	copy(out, stages)
	return m.lastPreview[jobID], out
}

func (m *Manager) worker(ctx context.Context, q chan int64) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			m.runGuarded(ctx, id)
		}
	}
}

// runGuarded runs one job with a panic backstop: a panic in the pipeline (a bad master, a decode bug)
// used to crash the ENTIRE engine process (the worker goroutine has no recover), which then reconciled
// every in-flight job as "interrupted by a server restart". Now a panic fails only its own job and the
// engine stays up for the others.
func (m *Manager) runGuarded(ctx context.Context, id int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("astrostack: job %d panicked: %v\n%s", id, r, debug.Stack())
			m.finishTerminal(id, store.JobFailed, fmt.Errorf("internal error (recovered): %v", r))
		}
	}()
	m.run(ctx, id)
}

func (m *Manager) run(ctx context.Context, id int64) {
	job, err := m.store.GetJob(ctx, id)
	if err != nil {
		return
	}
	var p RunRequest
	_ = json.Unmarshal(job.Params, &p)
	turnID := m.TurnFor(id) // "" unless this is a supervised/refine job with a conversation

	// A stacked job the user removed from the queue before it started is marked cancelled — skip it.
	if job.Status == store.JobCancelled {
		m.closeTurn(id, store.JobCancelled, "Cancelled before start.")
		return
	}

	// A resume checkpoint left by a prior pause (zero value for a fresh job).
	var cp resumeCheckpoint
	if len(job.Resume) > 0 {
		_ = json.Unmarshal(job.Resume, &cp)
	}

	// Serialize against any other job whose input roots overlap — ALL of them, not only the primary
	// (a transfer/free over a secondary multi-select folder must not race the stack), and
	// prefix-aware (a parent-folder transfer conflicts with a child-folder run).
	unlock := m.lockTarget(p.inputRoots()...)
	defer unlock()

	runCtx, cancel := context.WithCancel(ctx)
	gate := &pauseGate{}
	m.mu.Lock()
	m.cancels[id] = cancel
	m.pauses[id] = gate
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, id)
		delete(m.pauses, id)
		m.mu.Unlock()
	}()

	_ = m.store.SetJobRunning(ctx, id)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "starting"})

	// Resume fast path: a job paused after a SUCCESSFUL compute (only the S3 push failed) just re-pushes
	// the kept result — no recompute.
	if cp.Phase == phasePush {
		res := json.RawMessage(job.Result)
		if err := m.pushS3Run(runCtx, id, p, res); err != nil {
			m.settleS3Error(id, runCtx, phasePush, res, err, cp.Attempts)
			return
		}
		m.finishSucceeded(id, p, job.Result)
		return
	}

	// Full-S3 storage: pull the capture folders from S3 before processing so a run can work from files that
	// live only on S3 (idempotent — same-size local files are skipped, so a resumed pull is cheap). A
	// transient network failure PAUSES the job (resumable) rather than failing it. The low-disk staged mode
	// skips this whole-folder pull: the pipeline's InputStager downloads one wave at a time instead. A
	// denoise re-finish also skips it — it needs only the output tree (final.tif), hydrated in
	// executeDenoiseFinal via ensureRunDirLocal, never the raw captures.
	if p.wantsS3Storage() && !m.lowDiskActive(p) && p.DenoiseFinal == nil {
		if err := m.pullS3Inputs(runCtx, id, p); err != nil {
			m.settleS3Error(id, runCtx, phasePull, nil, err, cp.Attempts)
			return
		}
	}

	res, runErr := m.execute(runCtx, id, turnID, job.Kind, p, cp.pipelineResume(), gate)

	// A cooperative mid-stack pause returns *PausedError; a manual pause during a standalone transfer
	// returns transfer.ErrPaused. Either parks the job in the resumable paused state (Cause=manual, so the
	// auto-resume sweep never touches it) rather than failing it.
	if runErr != nil {
		// Low-disk staged input pull failed mid-compute: pause the compute phase (carrying the run's
		// id/outDir so resume reuses the output dir + skips finished channels). A transient S3 error
		// auto-resumes with backoff; a manual pause during staging (ErrPaused inside) stays paused. Checked
		// before PausedError/ErrPaused because a StagePullError unwraps to ErrPaused for a manual pause.
		var spe *pipeline.StagePullError
		if errors.As(runErr, &spe) {
			switch classifyS3Error(runCtx.Err(), spe.Err) {
			case outcomeCancel:
				m.finishTerminal(id, store.JobCancelled, spe.Err)
			case outcomePause:
				pcp := s3PauseCheckpoint(phaseCompute, spe.Err, cp.Attempts)
				pcp.RunID, pcp.OutDir = spe.RunID, spe.OutDir
				m.pauseJob(id, pcp, nil)
			default:
				m.finishTerminal(id, store.JobFailed, spe.Err)
			}
			return
		}
		var pe *pipeline.PausedError
		if errors.As(runErr, &pe) {
			m.pauseJob(id, resumeCheckpoint{Phase: phaseCompute, RunID: pe.RunID, OutDir: pe.OutDir,
				Cause: causeManual, Reason: "paused by you — will continue the remaining channels"}, nil)
			return
		}
		if errors.Is(runErr, transfer.ErrPaused) {
			m.pauseJob(id, resumeCheckpoint{Phase: phaseTransfer, Cause: causeManual,
				Reason: "paused by you — will resume the transfer"}, nil)
			return
		}
		// Terminal writes use a fresh context so they persist even if the run was cancelled.
		status := store.JobFailed
		if runCtx.Err() != nil || errors.Is(runErr, context.Canceled) {
			status = store.JobCancelled
		}
		m.finishTerminal(id, status, runErr)
		return
	}

	// A cancel that lands during the soft-fail finishing stages (GIMP/combine warn instead of
	// erroring) lets the pipeline limp through to a partial result with a nil error — that run must
	// finish as CANCELLED, not masquerade as a success. The partial result is kept: the stacked
	// channels on disk are real work the user may still want to inspect or refine.
	if runCtx.Err() != nil {
		_ = m.store.FinishJob(context.Background(), id, store.JobCancelled, resultBlob(res), "cancelled during the run — partial result kept")
		m.publish(Event{JobID: id, Status: store.JobCancelled, Step: "cancelled", Done: true})
		m.closeTurn(id, store.JobCancelled, "Cancelled — kept the channels finished so far.")
		return
	}

	// Full-S3 storage: after a successful run, push inputs+outputs to S3 (local copies are kept so a retry
	// can reuse them — freeing is the explicit "Remove local files" action). A transient push failure
	// PAUSES (results stay safe locally) so Continue re-uploads.
	if p.wantsS3Storage() {
		if err := m.pushS3Run(runCtx, id, p, res); err != nil {
			m.settleS3Error(id, runCtx, phasePush, res, err, cp.Attempts)
			return
		}
	}

	m.finishSucceeded(id, p, resultBlob(res))
}

func (m *Manager) execute(ctx context.Context, id int64, turnID, kind string, p RunRequest,
	resume *pipeline.ResumeState, gate *pauseGate) (any, error) {
	// S3 transfer / backup / restore jobs are intercepted before pipeline-mode parsing — they reuse the
	// whole progress/SSE stack but do not run Siril.
	if p.Transfer != nil {
		return m.runTransfer(ctx, id, p.Transfer)
	}
	if p.Backup != nil {
		return m.runBackup(ctx, id, p.Backup)
	}
	if p.Restore != nil {
		return m.runRestore(ctx, id, p.Restore)
	}
	if p.Path == "" {
		return nil, fmt.Errorf("job has no path")
	}
	mo, err := mode.ParseMode(kind)
	if err != nil {
		return nil, err
	}
	format, _ := mode.ParseFormat(p.Format)
	if format == "" {
		format = mode.FormatImage
	}
	preset := mode.For(mo)
	if p.DropWheelTransition != nil {
		preset.DropFilterWheelTransition = *p.DropWheelTransition
	}
	if p.ColorCalibration != nil {
		preset.ColorCalibration = *p.ColorCalibration
	}
	if p.Denoise != nil && !*p.Denoise {
		preset.DenoiseChroma, preset.DenoiseLum = 0, 0
		// Also skip the (slow, ~90-min CPU) GraXpert joint colour denoise — previously "denoise off" left
		// it running. The on-demand "Denoise final" action lets the user run GraXpert later on the host.
		preset.ColorDenoiseAI = false
	}
	if p.HaExcludeStars != nil {
		preset.HaExcludeStars = *p.HaExcludeStars
	}
	if p.Look != "" {
		preset.Look = p.Look
	}
	if p.Palette != "" {
		preset.Palette = p.Palette
	}
	if p.ForegroundFrame != "" {
		preset.ForegroundFrame = p.ForegroundFrame
	}
	if p.Orientation != "" {
		preset.Orientation = p.Orientation
	}
	if bg, ok := mode.BrightnessTarget(p.Brightness); ok {
		preset.BackgroundLevel = bg
	}
	if p.Supervise {
		preset.Supervise = true
	}
	if p.Tier != "" {
		preset.SuperviseTier = p.Tier
	}
	if p.MaxIters > 0 {
		preset.SuperviseMaxIters = p.MaxIters
	}
	if p.CometX1 > 0 && p.CometY1 > 0 && p.CometX2 > 0 && p.CometY2 > 0 {
		preset.CometX1, preset.CometY1 = p.CometX1, p.CometY1
		preset.CometX2, preset.CometY2 = p.CometX2, p.CometY2
	}
	// Fine tunable-knob overrides — the agent's (or an advanced user's) chosen parameters, applied
	// through the SAME clamp table the in-run supervisor uses, so no request can push processing
	// outside known-good ranges. What changed (and what was ignored) lands in the job stream.
	if len(p.Params) > 0 {
		pr, err := pipeline.ApplyParamPatch(&preset, p.Params)
		if err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if len(pr.Changed) > 0 {
			m.publish(Event{JobID: id, Line: "params: set " + strings.Join(pr.Changed, ", ") + " (tier " + pr.Tier + ")"})
		}
		if len(pr.Ignored) > 0 {
			m.publish(Event{JobID: id, Line: "params: ignored unknown knobs " + strings.Join(pr.Ignored, ", ")})
		}
	}
	solve, spcc := postprocess.SolveSpccFromConfig(m.cfg)
	gclient := gimp.New(m.cfg.GimpBin, m.cfg.GimpHost, m.cfg.GimpPort)
	graxRunner := graxpert.New(m.cfg.GraxpertBin, m.cfg.GraxpertURL).SetDefaults(m.cfg.GraxpertGPU, m.cfg.GraxpertBatch) // optional; skipped when binary absent
	starRunner := starnet.New(m.cfg.StarnetBin)                                                                          // optional; skipped when binary absent
	var superRunner *llm.Runner
	if p.Supervise || p.Refine != nil { // opt-in local-AI-agent finish (always on for a refine); nil → standard finish
		superRunner = llm.New(m.cfg.LLMBaseURL, m.cfg.LLMModel, m.cfg.LLMImageFormat).WithTimeout(m.cfg.LLMTimeout)
	}
	// Conversation steering for a supervised job: free-text nudges + stop (steer) and the expensive-step
	// gate (confirm). Both are nil when this job has no turn, leaving the autonomous path unchanged.
	steer := m.steerHook(turnID)
	confirm := m.confirmHook(turnID)
	grd := preset.Grade

	logRing := make([]logEntry, 0, 256)
	var (
		lastFlush  time.Time
		sinceFlush int
		stepPeak   int64 // peak subprocess RSS seen during the current step
		lastPct    int   // last progress + step, for the guaranteed final flush
		lastStep   string
	)
	flush := func(pct int, step string) {
		_ = m.store.UpdateJobProgress(ctx, id, pct, step, encodeLog(logRing))
		lastFlush, sinceFlush = time.Now(), 0
	}
	// Persist the final log tail on any exit (success, error, cancel) so a browser refresh keeps the
	// whole log even when the last lines never tripped the debounce. A fresh context survives a
	// cancelled run, matching the terminal writes in run().
	defer func() {
		_ = m.store.UpdateJobProgress(context.Background(), id, lastPct, lastStep, encodeLog(logRing))
	}()
	pipeProg := func(pr pipeline.Progress) {
		// Milestone preview: a labeled PNG saved at a processing step. It carries no Index/Total, so publish
		// it at the last real progress value (never yanking the % bar to 0) — it accumulates into the UI's
		// stage timeline, and also drives the single live-preview image via Preview.
		if pr.StagePreview != nil {
			m.publish(Event{JobID: id, Status: store.JobRunning, Progress: lastPct, Step: pr.Step,
				Preview: pr.StagePreview.PngPath, StagePreview: pr.StagePreview})
			return
		}
		pct := stepPercent(pr.Index, pr.Total)
		lastPct, lastStep = pct, pr.Step

		// Supervised-finish iteration: stream the completed pass (preview + tier + defects + scores) so
		// the UI shows the agent iterating live. Live-only (persistence is handled in the pipeline).
		if pr.Iteration != nil {
			m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step,
				Preview: pr.Iteration.PngPath, Iteration: pr.Iteration})
			// Mirror the pass into the job's conversation as a chat bubble (with its preview), so a
			// supervised run reads as a live agent turn. Direct publish → durable backlog, never dropped.
			if turnID != "" && m.turns != nil {
				m.turns.Publish(turnID, turns.Event{Kind: "thinking", Step: pr.Iteration.Index + 1,
					Text: iterSummary(pr.Iteration), Preview: pr.Iteration.PngPath})
			}
			return
		}

		// Live resource reading: track the step's peak RSS and stream it. Never persisted (live-only).
		// Skip an all-zero sample (subprocess already gone) so a streamed resource event always carries
		// a real RSS — that keeps the frontend's "is this a resource event?" check unambiguous.
		if pr.Sample != nil {
			if pr.Sample.RSSBytes == 0 {
				return
			}
			if pr.Sample.RSSBytes > stepPeak {
				stepPeak = pr.Sample.RSSBytes
			}
			m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step,
				RSSBytes: pr.Sample.RSSBytes, CPUPercent: pr.Sample.CPUPercent, PeakRSSBytes: stepPeak})
			return
		}

		// Log line: stamp it, ring-buffer it, persist on a debounce so a refresh keeps the tail.
		if pr.Line != "" {
			ts := time.Now().UnixMilli()
			logRing = append(logRing, logEntry{ts: ts, line: pr.Line})
			if len(logRing) > logCap {
				logRing = logRing[len(logRing)-logCap:]
			}
			sinceFlush++
			if sinceFlush >= flushEveryN || time.Since(lastFlush) >= flushInterval {
				flush(pct, pr.Step)
			}
			m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step,
				Line: pr.Line, Ts: ts, Preview: pr.Preview})
			return
		}

		// Step boundary (or a preview-only event): reset the per-step peak, persist, and stream.
		stepPeak = 0
		flush(pct, pr.Step)
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step, Preview: pr.Preview})
	}

	// A denoise-final job runs GraXpert AI denoise on the completed run's final image (on demand,
	// non-blocking, offloaded to the host GraXpert service when configured).
	if p.DenoiseFinal != nil {
		return m.executeDenoiseFinal(ctx, id, p, graxRunner, pipeProg)
	}
	// A refine job re-finishes an existing run under the supervisor instead of processing from scratch.
	if p.Refine != nil {
		return m.executeRefine(ctx, id, p, preset, gclient, graxRunner, starRunner, superRunner, solve, spcc, pipeProg, steer, confirm)
	}
	// A rerun re-runs an existing run from the stage an edited param requires, in place (non-supervised).
	if p.Rerun != nil {
		return m.executeRerun(ctx, id, p, preset, gclient, graxRunner, starRunner, solve, spcc, pipeProg)
	}

	switch mo {
	case mode.Planetary:
		// ProcessPlanetary runs lucky imaging then the optional supervised finish (re-tunes the
		// sharpen/stretch over the persisted masters); it routes Siril logs through pipeProg like the
		// deep-sky path, and returns the same flat planetary.Result.
		r, err := pipeline.ProcessPlanetary(ctx, pipeline.Options{
			InputDir: p.Path, OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			FfmpegBin: m.cfg.FfmpegBin, Preset: &preset,
			Supervisor: superRunner, JobID: id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal, // opt-in local-AI-agent finish
			OnProgress: pipeProg, Steer: steer, Confirm: confirm,
		})
		if err != nil {
			return nil, err
		}
		r.Outputs = m.appendVideo(ctx, id, format, r.Outputs)
		return r, nil

	case mode.Milkyway:
		r, err := pipeline.ProcessOSC(ctx, pipeline.Options{
			InputDir: p.Path, InputDirs: p.inputRoots(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Supervisor: superRunner, JobID: id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal, // opt-in local-AI-agent finish
			Solve: solve, Spcc: spcc, DarkDir: p.DarkDir, FlatDir: p.FlatDir, BiasDir: p.BiasDir,
			PhoneCalib: m.store, LibraryDir: m.cfg.LibraryDir, LibraryMirror: m.libPuller(ctx),
			CatalogDir: m.cfg.SirilCatalogDir, OnProgress: pipeProg, Steer: steer, Confirm: confirm,
		})
		if err != nil {
			return nil, err
		}
		if r.Final != nil {
			r.Final.Outputs = m.appendVideo(ctx, id, format, r.Final.Outputs)
		}
		return r, nil

	case mode.Livestack:
		// Live stacking: watch a source, incrementally stack, and finalize through the standard deep-sky
		// pipeline on Stop. The finalize options mirror the deepsky branch (incl. cross-session reuse) so
		// the published master is identical to a normal run; livestack.Run sets InputDir to the source's
		// local root (the watched dir, or the S3 download mirror).
		src, serr := m.liveSource(ctx, p)
		if serr != nil {
			return nil, serr
		}
		fopts := pipeline.Options{
			InputDir: src.LocalRoot(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Supervisor: superRunner,                                                          // opt-in local-AI-agent finish (nil → standard finish)
			JobID:      id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal, // persist supervised iterations against this job
			Library: m.store, LibraryDir: m.cfg.LibraryDir, LibraryMirror: m.libPuller(ctx), OnProgress: pipeProg, Steer: steer, Confirm: confirm,
			FilterMapping: p.FilterMap, Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir,
			Catalog: m.store,
		}
		if m.cfg.ReuseEnabled && !p.ReuseDisabled {
			fopts.RawCalib = m.store
			fopts.Deep = deepOptions(m.cfg)
			fopts.Reuse = pipeline.ReuseConfig{Provider: m.store, ConeDeg: m.cfg.ReuseConeDeg, Sessions: p.reuseSessions()}
		}
		var expSec float64
		if p.Live != nil {
			expSec = p.Live.ExposureSec
		}
		r, err := livestack.Run(ctx, livestack.Options{
			Source:       src,
			Finalize:     fopts,
			ExposureMs:   int64(expSec * 1000),
			Poll:         time.Duration(m.cfg.LivePollSec) * time.Second,
			Stability:    time.Duration(m.cfg.LiveStabilitySec) * time.Second,
			RestackEvery: m.cfg.LiveRestackEvery,
			MinInterval:  time.Duration(m.cfg.LiveMinIntervalSec) * time.Second,
		})
		if err != nil {
			return nil, err
		}
		if r != nil && r.Final != nil {
			// ctx (the user's Stop) is already cancelled here; the video is part of the post-Stop finish,
			// so render it on a detached context like the master rather than the cancelled runCtx.
			r.Final.Outputs = m.appendVideo(context.Background(), id, format, r.Final.Outputs)
		}
		return r, nil

	case mode.Comet:
		// Moving-comet mode: a dual star/comet stack + star-layer recomposite (pipeline.ProcessComet).
		r, err := pipeline.ProcessComet(ctx, pipeline.Options{
			InputDir: p.Path, InputDirs: p.inputRoots(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Supervisor: superRunner, JobID: id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal, // opt-in local-AI-agent finish
			Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir, FilterMapping: p.FilterMap,
			Catalog: m.store, OnProgress: pipeProg, Steer: steer, Confirm: confirm,
		})
		if err != nil {
			return nil, err
		}
		if r.Final != nil {
			r.Final.Outputs = m.appendVideo(ctx, id, format, r.Final.Outputs)
		}
		return r, nil

	default: // deepsky / nebula — combine() builds the postprocess options from the preset + solve/spcc
		opts := pipeline.Options{
			InputDir: p.Path, InputDirs: p.inputRoots(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Supervisor: superRunner,                                                          // opt-in local-AI-agent finish (nil → standard finish)
			JobID:      id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal, // persist supervised iterations against this job
			Library: m.store, LibraryDir: m.cfg.LibraryDir, LibraryMirror: m.libPuller(ctx), OnProgress: pipeProg, Steer: steer, Confirm: confirm,
			FilterMapping: p.FilterMap, Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir,
			Catalog:      m.store, // always record the run so its frames become reusable
			CalibExclude: p.CalibExclude,
			// Pause/resume: reuse a paused run's output dir (skip already-stacked channels) and let the user
			// pause mid-stack at a channel boundary. Only the deep-sky path honors mid-stack pause.
			Resume:         resume,
			PauseRequested: gate.requested,
		}
		if m.cfg.ReuseEnabled && !p.ReuseDisabled {
			opts.RawCalib = m.store // pool raw bias/darks across sessions into deep masters
			opts.Deep = deepOptions(m.cfg)
			opts.Reuse = pipeline.ReuseConfig{
				Provider: m.store, ConeDeg: m.cfg.ReuseConeDeg, Sessions: p.reuseSessions(),
			}
		}
		// Low-disk staged S3 mode: supply inputs on demand (scan remotely, download/free one wave at a time)
		// instead of the whole-folder pull run() skipped for this run.
		if m.lowDiskActive(p) {
			st, serr := m.newS3Stager(id, p)
			if serr != nil {
				return nil, fmt.Errorf("low-disk stager: %w", serr)
			}
			opts.Stager = st
		}
		r, err := pipeline.Process(ctx, opts)
		if err != nil {
			return nil, err
		}
		if r.Final != nil {
			r.Final.Outputs = m.appendVideo(ctx, id, format, r.Final.Outputs)
		}
		return r, nil
	}
}

// executeRefine re-finishes an already-completed run under the AI supervisor (no re-stack): it forces
// supervision on, applies the refine ceiling/iteration cap, runs the finish-only pipeline from the
// run's on-disk masters, then returns the refreshed run record so JobView shows the new final +
// iterations. Tier C (re-stack from raws) is not wired here yet, so the loop caps at Tier B.
func (m *Manager) executeRefine(ctx context.Context, id int64, p RunRequest, preset mode.Preset,
	gclient *gimp.Client, grax *graxpert.Runner, star *starnet.Runner, super *llm.Runner,
	solve siril.SolveOptions, spcc siril.SpccOptions, pipeProg func(pipeline.Progress),
	steer func() (string, bool), confirm func(context.Context, string, []string) (string, bool)) (any, error) {
	// A refine reads the run's on-disk masters/run.json; re-hydrate them from S3 when they were freed.
	if err := m.ensureRunDirLocal(ctx, id, p, p.Refine.RunDir); err != nil {
		return nil, err
	}
	preset.Supervise = true
	preset.SuperviseTier = p.Refine.Tier
	if p.Refine.MaxIters > 0 {
		preset.SuperviseMaxIters = p.Refine.MaxIters
	}
	if len(p.Refine.Params) > 0 {
		pr, err := pipeline.ApplyParamPatch(&preset, p.Refine.Params)
		if err != nil {
			return nil, fmt.Errorf("invalid refine params: %w", err)
		}
		if len(pr.Changed) > 0 {
			m.publish(Event{JobID: id, Line: "refine params: set " + strings.Join(pr.Changed, ", ")})
		}
	}
	opts := pipeline.Options{
		OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
		Preset: &preset, Gimp: gclient, Graxpert: grax, Starnet: star,
		Supervisor: super, JobID: id, FinishIterStore: m.store, FinishPriors: m.priors(), Goal: p.Goal,
		Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir, OnProgress: pipeProg,
		Steer: steer, Confirm: confirm,
	}
	final, err := pipeline.RefineExistingRun(ctx, opts, p.Refine.RunDir)
	if err != nil {
		return nil, err
	}
	format, _ := mode.ParseFormat(p.Format)
	// The deep-sky family rewrites run.json with the full record (channels + masters + the refreshed final +
	// iterations); reopen it so JobView shows the richest view. The single-stage modes (comet / milkyway /
	// planetary) keep no pipeline run.json at the run dir, so return the finish result directly — reading a
	// missing run.json would fail the refine even though the supervised finish just succeeded.
	switch preset.Mode {
	case mode.Deepsky, mode.Nebula, mode.Livestack:
		res, rerr := pipeline.ReadRunResult(p.Refine.RunDir)
		if rerr != nil {
			return nil, rerr
		}
		if res.Final != nil {
			res.Final.Outputs = m.appendVideo(ctx, id, format, res.Final.Outputs)
		}
		return res, nil
	default:
		if final != nil {
			final.Outputs = m.appendVideo(ctx, id, format, final.Outputs)
		}
		return final, nil
	}
}

// executeRerun re-runs an already-completed deepsky/nebula run from the stage a parameter edit requires
// (pipeline.RerunFromStage), overwriting the run in place — the manual, non-supervised counterpart of
// executeRefine. It builds the same processing options a normal run uses, so a Tier-C re-entry can
// re-stack from the raw frames and reuse the calibration library; it returns the refreshed run record so
// JobView shows the new final + previews.
func (m *Manager) executeRerun(ctx context.Context, id int64, p RunRequest, preset mode.Preset,
	gclient *gimp.Client, grax *graxpert.Runner, star *starnet.Runner,
	solve siril.SolveOptions, spcc siril.SpccOptions, pipeProg func(pipeline.Progress)) (any, error) {
	// A Tier-A/B rerun reuses the run's on-disk masters; re-hydrate the output tree from S3 when it was freed
	// (a Tier-C re-stack additionally gets its raw inputs from the whole-folder pull in run()).
	if err := m.ensureRunDirLocal(ctx, id, p, p.Rerun.RunDir); err != nil {
		return nil, err
	}
	preset.Supervise = false // a manual rerun never invokes the agent
	grd := preset.Grade
	opts := pipeline.Options{
		InputDir: p.Path, InputDirs: p.inputRoots(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
		Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: grax, Starnet: star,
		JobID: id, Library: m.store, LibraryDir: m.cfg.LibraryDir, OnProgress: pipeProg,
		FilterMapping: p.FilterMap, Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir,
		Catalog: m.store, CalibExclude: p.CalibExclude,
	}
	if m.cfg.ReuseEnabled && !p.ReuseDisabled {
		opts.RawCalib = m.store // Tier-C re-stack pools raw bias/darks into deep masters, like a normal run
		opts.Deep = deepOptions(m.cfg)
		opts.Reuse = pipeline.ReuseConfig{Provider: m.store, ConeDeg: m.cfg.ReuseConeDeg, Sessions: p.reuseSessions()}
	}
	res, err := pipeline.RerunFromStage(ctx, opts, p.Rerun.RunDir, p.Rerun.Params, p.Rerun.Stage)
	if err != nil {
		return nil, err
	}
	format, _ := mode.ParseFormat(p.Format)
	if res != nil && res.Final != nil {
		res.Final.Outputs = m.appendVideo(ctx, id, format, res.Final.Outputs)
	}
	return res, nil
}

// executeDenoiseFinal runs GraXpert AI denoise on a completed run's final.tif and returns a result that
// surfaces the denoised image (plus a PNG for the browser). When ASTRO_GRAXPERT_URL is set the runner
// offloads to the native host GraXpert service (faster + non-blocking). NOTE: GraXpert's denoise model is
// CoreML-incompatible, so this is CPU-bound — "faster + on demand", not instant.
func (m *Manager) executeDenoiseFinal(ctx context.Context, id int64, p RunRequest, grax *graxpert.Runner, pipeProg func(pipeline.Progress)) (any, error) {
	runDir := p.DenoiseFinal.RunDir
	// Re-hydrate the run's output tree from S3 when its results were freed (final.tif lives under output/,
	// which the input pull never fetches); no-op when already local. Idempotent, so a retry reuses on-disk files.
	if err := m.ensureRunDirLocal(ctx, id, p, runDir); err != nil {
		return nil, err
	}
	if err := grax.Available(ctx); err != nil {
		return nil, fmt.Errorf("GraXpert unavailable (set GRAXPERT_BIN, or run `just run-graxpert-service` + ASTRO_GRAXPERT_URL): %w", err)
	}
	src := filepath.Join(runDir, "final.tif")
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("run has no final.tif to denoise in %s", runDir)
	}
	out := filepath.Join(runDir, "final_denoised.tif")
	pipeProg(pipeline.Progress{Step: "denoising final", Line: "GraXpert AI denoise on the final image…"})
	fwd := func(pr graxpert.Progress) { pipeProg(pipeline.Progress{Step: "denoising final", Line: pr.Line}) }
	if err := grax.Denoise(ctx, src, out, graxpert.DenoiseOptions{}, fwd); err != nil {
		return nil, fmt.Errorf("denoise final: %w", err)
	}
	if _, err := os.Stat(out); err != nil {
		return nil, fmt.Errorf("denoise final: GraXpert produced no output")
	}
	// Export a PNG the browser can show (Siril: load the denoised TIFF, save a PNG next to it). Non-fatal:
	// the TIFF is always returned even if the PNG step fails.
	if _, err := m.runner.Run(ctx, runDir, "load final_denoised\nsavepng final_denoised\n", nil); err != nil {
		pipeProg(pipeline.Progress{Step: "denoising final", Line: "PNG export skipped: " + err.Error()})
	}
	outputs := make([]string, 0, 2)
	png := filepath.Join(runDir, "final_denoised.png")
	if _, err := os.Stat(png); err == nil {
		outputs = append(outputs, png)
	}
	outputs = append(outputs, out)
	return &pipeline.Result{
		OutputDir: runDir,
		Object:    filepath.Base(filepath.Dir(runDir)),
		Final:     &postprocess.Result{Outputs: outputs, Notes: []string{"GraXpert AI denoise applied to the final image"}},
	}, nil
}

// deepOptions builds the raw-calibration pool window from config: a temperature tolerance and an
// optional dark recency cutoff (darks older than ReuseDarkRecencyDays are excluded; 0 = unbounded).
func deepOptions(cfg *config.Config) calib.DeepOptions {
	return calib.DeepOptions{TempTolC: cfg.ReuseTempTolC, DarkSinceMs: cfg.DarkSinceMs()}
}

// liveSource builds the frame source for a live-stacking job: a local directory (default) or an S3
// bucket. S3 credentials come from the resolved pipeline config (default UI connection, else the host
// environment) — never the request. Bucket/prefix still come per-job from the request.
func (m *Manager) liveSource(ctx context.Context, p RunRequest) (source.Source, error) {
	if p.Live == nil || p.Live.SourceKind == "" || p.Live.SourceKind == "local" {
		return source.NewLocal(p.Path)
	}
	if p.Live.SourceKind == "s3" {
		if p.Live.Bucket == "" {
			return nil, fmt.Errorf("live s3 source: bucket is required")
		}
		key := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(p.Live.Bucket + "_" + p.Live.Prefix)
		dl := filepath.Join(m.cfg.WorkDir, "live_s3", key)
		sc := m.s3ConfigResolved(ctx)
		return source.NewS3(source.S3Config{
			Endpoint: sc.Endpoint, Region: sc.Region,
			AccessKeyID: sc.AccessKeyID, SecretKey: sc.SecretKey, UseSSL: sc.UseSSL,
			Bucket: p.Live.Bucket, Prefix: p.Live.Prefix, DownloadDir: dl,
		})
	}
	return nil, fmt.Errorf("unknown live source kind %q (want: local, s3)", p.Live.SourceKind)
}

// appendVideo renders a Ken-Burns MP4 from the final PNG when the format requests video.
func (m *Manager) appendVideo(ctx context.Context, id int64, format mode.Format, outputs []string) []string {
	if !format.WantsVideo() {
		return outputs
	}
	var png string
	for _, o := range outputs {
		if strings.HasSuffix(o, ".png") {
			png = o
			break
		}
	}
	if png == "" {
		return outputs
	}
	// Held at 99% (not 100%): the still image is done but the MP4 is still rendering — 100% is reserved
	// for the job's "done" event so the bar never reads complete while work remains.
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 99, Step: "rendering video"})
	_ = m.store.UpdateJobProgress(ctx, id, 99, "rendering video", "")
	mp4 := strings.TrimSuffix(png, ".png") + ".mp4"
	if err := videoout.Render(ctx, m.cfg.FfmpegBin, png, mp4, videoout.DefaultOptions()); err != nil {
		return outputs
	}
	return append(outputs, mp4)
}
