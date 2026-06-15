// Package job runs pipeline work in a background worker pool, persists each job's lifecycle to
// Postgres, and publishes live progress to subscribers (consumed by the API's SSE endpoint).
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/planetary"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
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
	Done     bool   `json:"done,omitempty"`
}

// Manager owns the worker pool and the subscriber registry.
type Manager struct {
	store  *store.Store
	runner *siril.Runner
	cfg    *config.Config

	queue chan int64
	mu    sync.Mutex
	subs  map[int64][]chan Event
}

type params struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Format string `json:"format"`
}

// NewManager creates a Manager with a bounded queue.
func NewManager(st *store.Store, runner *siril.Runner, cfg *config.Config) *Manager {
	return &Manager{
		store:  st,
		runner: runner,
		cfg:    cfg,
		queue:  make(chan int64, 256),
		subs:   map[int64][]chan Event{},
	}
}

// Start launches n worker goroutines that stop when ctx is cancelled.
func (m *Manager) Start(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go m.worker(ctx)
	}
}

// Enqueue creates a session and a queued job (kind = mode), then schedules it. Returns the job id.
func (m *Manager) Enqueue(ctx context.Context, modeStr, formatStr, path string) (int64, error) {
	sessionID, err := m.store.CreateSession(ctx, path, "")
	if err != nil {
		return 0, err
	}
	p, _ := json.Marshal(params{Path: path, Mode: modeStr, Format: formatStr})
	id, err := m.store.CreateJob(ctx, sessionID, modeStr, p)
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
	var p params
	_ = json.Unmarshal(job.Params, &p)

	_ = m.store.SetJobRunning(ctx, id)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "starting"})

	res, runErr := m.execute(ctx, id, job.Kind, p)
	if runErr != nil {
		_ = m.store.FinishJob(ctx, id, store.JobFailed, nil, runErr.Error())
		m.publish(Event{JobID: id, Status: store.JobFailed, Step: runErr.Error(), Done: true})
		return
	}
	result, _ := json.Marshal(res)
	_ = m.store.FinishJob(ctx, id, store.JobSucceeded, result, "")
	m.publish(Event{JobID: id, Status: store.JobSucceeded, Progress: 100, Step: "done", Done: true})
}

func (m *Manager) execute(ctx context.Context, id int64, kind string, p params) (any, error) {
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
	gclient := gimp.New(m.cfg.GimpBin, m.cfg.GimpHost, m.cfg.GimpPort)
	grd := preset.Grade

	pipeProg := func(pr pipeline.Progress) {
		pct := 0
		if pr.Total > 0 {
			pct = pr.Index * 100 / pr.Total
		}
		if pr.Line == "" {
			_ = m.store.UpdateJobProgress(ctx, id, pct, pr.Step, "")
		}
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step, Line: pr.Line})
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
			Grade: &grd, Preset: &preset, Gimp: gclient, OnProgress: pipeProg,
		})
		if err != nil {
			return nil, err
		}
		if r.Final != nil {
			r.Final.Outputs = m.appendVideo(ctx, id, format, r.Final.Outputs)
		}
		return r, nil

	default: // deepsky / nebula
		pp := postprocess.Options{
			BackgroundExtraction: true, BackgroundDegree: preset.BackgroundDegree,
			Saturation: preset.Saturation, Formats: []string{"png", "tif"},
		}
		r, err := pipeline.Process(ctx, pipeline.Options{
			InputDir: p.Path, OutputDir: m.cfg.OutputDir, WorkDir: m.cfg.WorkDir, Runner: m.runner,
			Grade: &grd, Postprocess: &pp, Preset: &preset, Gimp: gclient,
			Library: m.store, LibraryDir: m.cfg.LibraryDir, OnProgress: pipeProg,
		})
		if err != nil {
			return nil, err
		}
		if r.Final != nil {
			r.Final.Outputs = m.appendVideo(ctx, id, format, r.Final.Outputs)
		}
		return r, nil
	}
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
