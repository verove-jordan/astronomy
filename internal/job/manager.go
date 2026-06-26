// Package job runs pipeline work in a background worker pool, persists each job's lifecycle to
// Postgres, and publishes live progress to subscribers (consumed by the API's SSE endpoint).
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/starnet"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/videoout"
)

// Event is a progress update for a job, streamed to subscribers.
type Event struct {
	JobID    int64  `json:"job_id"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Step     string `json:"step"`
	Line     string `json:"line,omitempty"`
	Preview  string `json:"preview,omitempty"` // a preview PNG path produced mid-run
	Done     bool   `json:"done,omitempty"`
}

// Manager owns the worker pool and the subscriber registry.
type Manager struct {
	store  *store.Store
	runner *siril.Runner
	cfg    *config.Config

	queue   chan int64
	mu      sync.Mutex
	subs    map[int64][]chan Event
	cancels map[int64]context.CancelFunc // cancel funcs for in-flight jobs (kill support)
}

// RunRequest is the user-supplied job configuration (also persisted as the job's params JSON).
type RunRequest struct {
	Path                string            `json:"path"`
	Mode                string            `json:"mode"`
	Format              string            `json:"format"`
	FilterMap           map[string]string `json:"filter_map,omitempty"`            // detected/known filter → chosen channel ("ignore" drops)
	DropWheelTransition *bool             `json:"drop_wheel_transition,omitempty"` // override preset default
	ColorCalibration    *bool             `json:"color_calibration,omitempty"`     // override preset default
	Denoise             *bool             `json:"denoise,omitempty"`               // false disables denoise

	// Reuse controls cross-session light reuse. ReuseDisabled turns it off for this run; ReuseSessions
	// (when non-empty) restricts the folded-in prior data to the listed session ids (the user's
	// selection from the auto-discovered preview). Empty + enabled → fold in all matching sessions.
	ReuseDisabled bool    `json:"reuse_disabled,omitempty"`
	ReuseSessions []int64 `json:"reuse_sessions,omitempty"`
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
		store:   st,
		runner:  runner,
		cfg:     cfg,
		queue:   make(chan int64, 256),
		subs:    map[int64][]chan Event{},
		cancels: map[int64]context.CancelFunc{},
	}
}

// Start launches n worker goroutines that stop when ctx is cancelled.
func (m *Manager) Start(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go m.worker(ctx)
	}
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
	select {
	case m.queue <- id:
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
		cancel()
	}
	return ok
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

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
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
	solve := siril.SolveOptions{FocalMM: m.cfg.FocalLenMM, PixelUm: m.cfg.PixelSizeUm, Catalog: m.cfg.PlateSolveCatalog}
	spcc := siril.SpccOptions{
		MonoSensor: m.cfg.SpccMonoSensor, RFilter: m.cfg.SpccRFilter, GFilter: m.cfg.SpccGFilter,
		BFilter: m.cfg.SpccBFilter, WhiteRef: m.cfg.SpccWhiteRef,
	}
	gclient := gimp.New(m.cfg.GimpBin, m.cfg.GimpHost, m.cfg.GimpPort)
	graxRunner := graxpert.New(m.cfg.GraxpertBin) // optional; skipped when binary absent
	starRunner := starnet.New(m.cfg.StarnetBin)   // optional; skipped when binary absent
	grd := preset.Grade

	logRing := make([]string, 0, 256)
	pipeProg := func(pr pipeline.Progress) {
		pct := 0
		if pr.Total > 0 {
			pct = pr.Index * 100 / pr.Total
		}
		if pr.Line != "" {
			logRing = append(logRing, pr.Line)
			if len(logRing) > 200 {
				logRing = logRing[len(logRing)-200:]
			}
		} else { // step boundary: persist progress + the current log tail
			_ = m.store.UpdateJobProgress(ctx, id, pct, pr.Step, strings.Join(logRing, "\n"))
		}
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step, Line: pr.Line, Preview: pr.Preview})
	}

	switch mo {
	case mode.Planetary:
		r, err := planetary.Process(ctx, m.runner, m.cfg.FfmpegBin, p.Path, m.cfg.WorkDir, m.cfg.OutputDir, preset.Planetary,
			func(sp siril.Progress) {
				if sp.Line == "" {
					m.publish(Event{JobID: id, Status: store.JobRunning, Step: sp.Line})
				}
			})
		if err != nil {
			return nil, err
		}
		r.Outputs = m.appendVideo(ctx, id, format, r.Outputs)
		return r, nil

	case mode.Milkyway:
		r, err := pipeline.ProcessOSC(ctx, pipeline.Options{
			InputDir: p.Path, OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
			Solve: solve, Spcc: spcc, CatalogDir: m.cfg.SirilCatalogDir, OnProgress: pipeProg,
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
			InputDir: p.Path, OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Preset: &preset, Gimp: gclient, Graxpert: graxRunner, Starnet: starRunner,
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
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 100, Step: "rendering video"})
	_ = m.store.UpdateJobProgress(ctx, id, 100, "rendering video", "")
	mp4 := strings.TrimSuffix(png, ".png") + ".mp4"
	if err := videoout.Render(ctx, m.cfg.FfmpegBin, png, mp4, videoout.DefaultOptions()); err != nil {
		return outputs
	}
	return append(outputs, mp4)
}
