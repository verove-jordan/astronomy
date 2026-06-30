// Package job runs pipeline work in a background worker pool, persists each job's lifecycle to
// Postgres, and publishes live progress to subscribers (consumed by the API's SSE endpoint).
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/source"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/store"
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
type Manager struct {
	store  *store.Store
	runner *siril.Runner
	cfg    *config.Config

	queue    chan int64
	seqQueue chan int64 // sequential lane: stacked "Add to queue" jobs run one-at-a-time, auto-advancing
	mu       sync.Mutex
	subs     map[int64][]chan Event
	cancels  map[int64]context.CancelFunc // cancel funcs for in-flight jobs (kill support)

	pathMu    sync.Mutex
	pathLocks map[string]*sync.Mutex // serializes jobs sharing an input dir (shared library/output)
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
	Brightness      string `json:"brightness,omitempty"`
	DarkDir         string `json:"dark_dir,omitempty"`
	FlatDir         string `json:"flat_dir,omitempty"`
	BiasDir         string `json:"bias_dir,omitempty"`

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

	// Sequential routes this job into the single-worker queue lane so stacked "Add to queue" jobs run
	// one-at-a-time in submission order, auto-advancing — instead of the parallel pool. Default false.
	Sequential bool `json:"sequential,omitempty"`
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

// NewManager creates a Manager with a bounded queue.
func NewManager(st *store.Store, runner *siril.Runner, cfg *config.Config) *Manager {
	return &Manager{
		store:     st,
		runner:    runner,
		cfg:       cfg,
		queue:     make(chan int64, 256),
		seqQueue:  make(chan int64, 256),
		subs:      map[int64][]chan Event{},
		cancels:   map[int64]context.CancelFunc{},
		pathLocks: map[string]*sync.Mutex{},
	}
}

// lockTarget serializes jobs that share an input directory so a new run cannot race a still-running one
// on the shared calibration library and output directory. Jobs over different inputs run concurrently.
// It blocks until the lock is free and returns the unlock function.
func (m *Manager) lockTarget(path string) func() {
	key := filepath.Clean(path)
	m.pathMu.Lock()
	mu := m.pathLocks[key]
	if mu == nil {
		mu = &sync.Mutex{}
		m.pathLocks[key] = mu
	}
	m.pathMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// Start launches n parallel worker goroutines (draining the main queue) plus one dedicated worker for
// the sequential lane, all stopping when ctx is cancelled. The single seq worker is what makes stacked
// "Add to queue" jobs run strictly one-at-a-time in submission order, auto-advancing.
func (m *Manager) Start(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go m.worker(ctx, m.queue)
	}
	go m.worker(ctx, m.seqQueue)
}

// Enqueue creates a session and a queued job (kind = mode), then schedules it. Returns the job id.
func (m *Manager) Enqueue(ctx context.Context, req RunRequest) (int64, error) {
	sessionID, err := m.store.CreateSession(ctx, req.Path, "")
	if err != nil {
		return 0, err
	}
	p, _ := json.Marshal(req)
	id, err := m.store.CreateJob(ctx, sessionID, req.Mode, p)
	if err != nil {
		return 0, err
	}
	target := m.queue
	if req.Sequential {
		target = m.seqQueue // run after the rest of the chain, one-at-a-time
	}
	select {
	case target <- id:
	default:
		return 0, fmt.Errorf("job queue is full")
	}
	return id, nil
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
	// Not running: if it is still queued, mark it cancelled so the worker skips it when dequeued. This is
	// "remove from queue" for a stacked job that has not started yet.
	job, err := m.store.GetJob(context.Background(), id)
	if err != nil || job.Status != store.JobQueued {
		return false
	}
	if err := m.store.FinishJob(context.Background(), id, store.JobCancelled, nil, "cancelled before start"); err != nil {
		return false
	}
	m.publish(Event{JobID: id, Status: store.JobCancelled, Step: "cancelled", Done: true})
	return true
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
	for _, ch := range m.subs[e.JobID] {
		select {
		case ch <- e:
		default: // drop if the subscriber is slow; never block the worker
		}
	}
}

func (m *Manager) worker(ctx context.Context, q chan int64) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-q:
			m.run(ctx, id)
		}
	}
}

func (m *Manager) run(ctx context.Context, id int64) {
	job, err := m.store.GetJob(ctx, id)
	if err != nil {
		return
	}
	var p RunRequest
	_ = json.Unmarshal(job.Params, &p)

	// A stacked job the user removed from the queue before it started is marked cancelled — skip it.
	if job.Status == store.JobCancelled {
		return
	}

	// Serialize against any other run over the same input dir (the user's "careful about other running
	// runner"): block here until that run releases the shared library/output before we touch them.
	unlock := m.lockTarget(p.Path)
	defer unlock()

	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancels[id] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.cancels, id)
		m.mu.Unlock()
	}()

	_ = m.store.SetJobRunning(ctx, id)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "starting"})

	res, runErr := m.execute(runCtx, id, job.Kind, p)
	// Terminal writes use a fresh context so they persist even if the run was cancelled.
	if runErr != nil {
		status := store.JobFailed
		if runCtx.Err() != nil || errors.Is(runErr, context.Canceled) {
			status = store.JobCancelled
		}
		_ = m.store.FinishJob(context.Background(), id, status, nil, runErr.Error())
		m.publish(Event{JobID: id, Status: status, Step: runErr.Error(), Done: true})
		return
	}
	result, _ := json.Marshal(res)
	_ = m.store.FinishJob(context.Background(), id, store.JobSucceeded, result, "")
	m.publish(Event{JobID: id, Status: store.JobSucceeded, Progress: 100, Step: "done", Done: true})
}

func (m *Manager) execute(ctx context.Context, id int64, kind string, p RunRequest) (any, error) {
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
	}
	if p.HaExcludeStars != nil {
		preset.HaExcludeStars = *p.HaExcludeStars
	}
	if p.Look != "" {
		preset.Look = p.Look
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
	if p.CometX1 > 0 && p.CometY1 > 0 && p.CometX2 > 0 && p.CometY2 > 0 {
		preset.CometX1, preset.CometY1 = p.CometX1, p.CometY1
		preset.CometX2, preset.CometY2 = p.CometX2, p.CometY2
	}
	solve := siril.SolveOptions{FocalMM: m.cfg.FocalLenMM, PixelUm: m.cfg.PixelSizeUm, Catalog: m.cfg.PlateSolveCatalog}
	spcc := siril.SpccOptions{
		MonoSensor: m.cfg.SpccMonoSensor, OSCSensor: m.cfg.NightscapeOSCSensor,
		RFilter: m.cfg.SpccRFilter, GFilter: m.cfg.SpccGFilter,
		BFilter: m.cfg.SpccBFilter, WhiteRef: m.cfg.SpccWhiteRef,
	}
	gclient := gimp.New(m.cfg.GimpBin, m.cfg.GimpHost, m.cfg.GimpPort)
	graxRunner := graxpert.New(m.cfg.GraxpertBin) // optional; skipped when binary absent
	starRunner := starnet.New(m.cfg.StarnetBin)   // optional; skipped when binary absent
	var superRunner *llm.Runner
	if p.Supervise { // opt-in local-AI-agent finish; nil → standard finish
		superRunner = llm.New(m.cfg.LLMBaseURL, m.cfg.LLMModel, m.cfg.LLMImageFormat)
	}
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
		pct := stepPercent(pr.Index, pr.Total)
		lastPct, lastStep = pct, pr.Step

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

	switch mo {
	case mode.Planetary:
		r, err := planetary.Process(ctx, m.runner, m.cfg.FfmpegBin, p.Path, m.cfg.WorkDir, m.cfg.OutputDir, preset.Planetary,
			func(sp siril.Progress) {
				// Route through pipeProg so planetary gets the same timestamped, refresh-proof logs
				// and live resource readout as the deep-sky path.
				pipeProg(pipeline.Progress{Step: "planetary", Line: sp.Line, Sample: sp.Sample})
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
			Solve: solve, Spcc: spcc, DarkDir: p.DarkDir, FlatDir: p.FlatDir, BiasDir: p.BiasDir,
			CatalogDir: m.cfg.SirilCatalogDir, OnProgress: pipeProg,
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
		src, serr := m.liveSource(p)
		if serr != nil {
			return nil, serr
		}
		fopts := pipeline.Options{
			InputDir: src.LocalRoot(), OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Supervisor: superRunner,                  // opt-in local-AI-agent finish (nil → standard finish)
			JobID:      id, FinishIterStore: m.store, // persist supervised iterations against this job
			Library: m.store, LibraryDir: m.cfg.LibraryDir, OnProgress: pipeProg,
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
			Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir, FilterMapping: p.FilterMap,
			Catalog: m.store, OnProgress: pipeProg,
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
			Supervisor: superRunner,                  // opt-in local-AI-agent finish (nil → standard finish)
			JobID:      id, FinishIterStore: m.store, // persist supervised iterations against this job
			Library: m.store, LibraryDir: m.cfg.LibraryDir, OnProgress: pipeProg,
			FilterMapping: p.FilterMap, Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir,
			Catalog: m.store, // always record the run so its frames become reusable
		}
		if m.cfg.ReuseEnabled && !p.ReuseDisabled {
			opts.RawCalib = m.store // pool raw bias/darks across sessions into deep masters
			opts.Deep = deepOptions(m.cfg)
			opts.Reuse = pipeline.ReuseConfig{
				Provider: m.store, ConeDeg: m.cfg.ReuseConeDeg, Sessions: p.reuseSessions(),
			}
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

// deepOptions builds the raw-calibration pool window from config: a temperature tolerance and an
// optional dark recency cutoff (darks older than ReuseDarkRecencyDays are excluded; 0 = unbounded).
func deepOptions(cfg *config.Config) calib.DeepOptions {
	return calib.DeepOptions{TempTolC: cfg.ReuseTempTolC, DarkSinceMs: cfg.DarkSinceMs()}
}

// liveSource builds the frame source for a live-stacking job: a local directory (default) or an S3
// bucket. S3 credentials come from config (the host environment), never the request.
func (m *Manager) liveSource(p RunRequest) (source.Source, error) {
	if p.Live == nil || p.Live.SourceKind == "" || p.Live.SourceKind == "local" {
		return source.NewLocal(p.Path)
	}
	if p.Live.SourceKind == "s3" {
		if p.Live.Bucket == "" {
			return nil, fmt.Errorf("live s3 source: bucket is required")
		}
		key := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(p.Live.Bucket + "_" + p.Live.Prefix)
		dl := filepath.Join(m.cfg.WorkDir, "live_s3", key)
		return source.NewS3(source.S3Config{
			Endpoint: m.cfg.S3Endpoint, Region: m.cfg.S3Region,
			AccessKeyID: m.cfg.S3AccessKeyID, SecretKey: m.cfg.S3SecretAccessKey, UseSSL: m.cfg.S3UseSSL,
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
