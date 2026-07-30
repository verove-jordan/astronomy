package job

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/sysmon"
)

// liveStats is one running job's current progress position plus its per-step tool peak — the
// single shared truth between the progress fan-out (writer) and the engine monitor / heartbeat
// (readers), so every resource or proof-of-life event rides at the job's live pct/step.
type liveStats struct {
	mu       sync.Mutex
	started  bool // no event yet → resource publishers must skip (a pct-0 event would yank the bar)
	pct      int
	step     string
	toolPeak int64
}

func (s *liveStats) setProgress(pct int, step string) {
	s.mu.Lock()
	s.started, s.pct, s.step = true, pct, step
	s.mu.Unlock()
}

func (s *liveStats) position() (pct int, step string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pct, s.step, s.started
}

// toolSample folds one tool subprocess reading into the step's peak. Tool samples are no longer
// published as events — the engine monitor is the single publisher of resource numbers.
func (s *liveStats) toolSample(rss int64) {
	s.mu.Lock()
	if rss > s.toolPeak {
		s.toolPeak = rss
	}
	s.mu.Unlock()
}

// takeToolPeak returns and resets the step's tool peak (read when a step closes, and at step
// boundaries for paths that emit no ✓ lines).
func (s *liveStats) takeToolPeak() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.toolPeak
	s.toolPeak = 0
	return p
}

// engineMonitor samples the WHOLE engine process tree (Siril, GraXpert, StarNet, the GIMP server
// and ffmpeg are all children of this process) once a second and publishes each registered compute
// job's live reading at that job's current position — so the job header stays live through the
// pure-Go/GIMP/ffmpeg phases the old per-tool sampling left frozen at a stale number. Refcounted:
// the first registered job starts the single sampler, the last release stops it. Known limit: a
// host-offloaded GraXpert (ASTRO_GRAXPERT_URL) runs outside this subtree and is not counted.
type engineMonitor struct {
	mu      sync.Mutex
	publish func(Event)
	jobs    map[int64]*engineJob
	stopFn  func()
	last    sysmon.Sample // latest engine-wide reading (the heartbeat's enrich source)
	cores   int
}

type engineJob struct {
	ls   *liveStats
	peak int64 // job-wide peak engine RSS (never reset per step)
}

func newEngineMonitor(publish func(Event)) *engineMonitor {
	return &engineMonitor{publish: publish, jobs: map[int64]*engineJob{}, cores: runtime.NumCPU()}
}

// register adds a running compute job; the first registration starts the engine-wide sampler.
func (em *engineMonitor) register(id int64, ls *liveStats) {
	em.mu.Lock()
	defer em.mu.Unlock()
	em.jobs[id] = &engineJob{ls: ls}
	if em.stopFn != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	mon := sysmon.Start(ctx, os.Getpid(), time.Second, em.onSample)
	em.stopFn = func() {
		cancel()
		mon.Stop()
	}
}

// release drops a finished job; the last release stops the sampler.
func (em *engineMonitor) release(id int64) {
	em.mu.Lock()
	defer em.mu.Unlock()
	delete(em.jobs, id)
	if len(em.jobs) == 0 && em.stopFn != nil {
		em.stopFn()
		em.stopFn = nil
	}
}

// onSample publishes one engine-wide reading per registered job, each at its own live position.
func (em *engineMonitor) onSample(s sysmon.Sample) {
	if s.RSSBytes == 0 {
		return // keep the frontend's "resource event ⇒ real rss_bytes" contract
	}
	type out struct {
		id   int64
		pct  int
		step string
		peak int64
	}
	em.mu.Lock()
	em.last = s
	outs := make([]out, 0, len(em.jobs))
	for id, j := range em.jobs {
		pct, step, ok := j.ls.position()
		if !ok {
			continue // job has produced no progress yet — a pct-0 event would yank its bar
		}
		if s.RSSBytes > j.peak {
			j.peak = s.RSSBytes
		}
		outs = append(outs, out{id, pct, step, j.peak})
	}
	publish, cores := em.publish, em.cores
	em.mu.Unlock()

	for _, o := range outs {
		publish(Event{JobID: o.id, Status: store.JobRunning, Progress: o.pct, Step: o.step,
			RSSBytes: s.RSSBytes, CPUPercent: s.CPUPercent, PeakRSSBytes: o.peak, CPUCores: cores})
	}
}

// liveNote is the heartbeat's resource suffix ("cpu 10.8/12 cores · rss 6.7 GiB"); "" before the
// first reading or when the sampler is idle.
func (em *engineMonitor) liveNote() string {
	em.mu.Lock()
	defer em.mu.Unlock()
	if em.last.RSSBytes == 0 {
		return ""
	}
	return fmt.Sprintf("cpu %.1f/%d cores · rss %s", em.last.CPUPercent/100, em.cores, humanBytes(em.last.RSSBytes))
}
