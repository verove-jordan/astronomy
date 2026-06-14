// Package job runs pipeline work in a background worker pool, persists each job's lifecycle to
// Postgres, and publishes live progress to subscribers (consumed by the API's SSE endpoint).
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/store"
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
	Path string `json:"path"`
	Out  string `json:"out,omitempty"`
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

// Enqueue creates a session and a queued job, then schedules it. Returns the job id.
func (m *Manager) Enqueue(ctx context.Context, kind, path string) (int64, error) {
	sessionID, err := m.store.CreateSession(ctx, path, "")
	if err != nil {
		return 0, err
	}
	p, _ := json.Marshal(params{Path: path})
	id, err := m.store.CreateJob(ctx, sessionID, kind, p)
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
	var library calib.MasterStore = m.store

	onProgress := func(pr pipeline.Progress) {
		pct := 0
		if pr.Total > 0 {
			pct = pr.Index * 100 / pr.Total
		}
		if pr.Line == "" { // step change — persist + publish
			_ = m.store.UpdateJobProgress(ctx, id, pct, pr.Step, "")
		}
		m.publish(Event{JobID: id, Status: store.JobRunning, Progress: pct, Step: pr.Step, Line: pr.Line})
	}

	switch kind {
	case "process", "":
		return pipeline.Process(ctx, pipeline.Options{
			InputDir:   p.Path,
			OutputDir:  m.cfg.OutputDir,
			WorkDir:    m.cfg.WorkDir,
			Runner:     m.runner,
			Library:    library,
			LibraryDir: m.cfg.LibraryDir,
			OnProgress: onProgress,
		})
	default:
		return nil, fmt.Errorf("unknown job kind %q", kind)
	}
}
