package job

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Run scratch (<WorkDir>/run_<stamp>) is tens of GB per run — registered frames, and whole
// union-canvas sequences under mosaic — and nothing used to delete it: succeeded, failed and
// crashed runs all left theirs behind until one day of runs filled the disk to 100% and wedged the
// engine host (Docker daemon included). The sweep reclaims stale run dirs on boot and after every
// terminal job, and only while NO job is in flight, so a live run's scratch is never touched
// whatever its age. The grace window keeps the most recent run inspectable for a while and protects
// a concurrent host-CLI run's fresh dir (the CLI shares WorkDir but not the in-flight counter).
const (
	workRunPrefix  = "run_"
	workRunStamp   = "20060102_150405"
	workSweepGrace = 2 * time.Hour
)

// staleWorkRuns picks, from work-dir entry names, the run_<stamp> dirs whose embedded start time is
// older than grace. Names that do not parse as a run stamp are never returned — the sweep must not
// guess at what it cannot date.
func staleWorkRuns(names []string, now time.Time, grace time.Duration) []string {
	var out []string
	for _, n := range names {
		rest, ok := strings.CutPrefix(n, workRunPrefix)
		if !ok {
			continue
		}
		t, err := time.ParseInLocation(workRunStamp, rest, time.Local)
		if err != nil {
			continue
		}
		if now.Sub(t) > grace {
			out = append(out, n)
		}
	}
	return out
}

// sweepWorkScratch deletes stale run scratch dirs while the engine is idle. Safe to call from any
// goroutine; ASTRO_KEEP_WORK=1 disables it for debugging sessions that need a dead run's scratch.
func (m *Manager) sweepWorkScratch() {
	if m.cfg.KeepWork || m.inFlight.Load() != 0 {
		return
	}
	entries, err := os.ReadDir(m.cfg.WorkDir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	for _, name := range staleWorkRuns(names, time.Now(), workSweepGrace) {
		if m.inFlight.Load() != 0 {
			return // a job started mid-sweep — its fresh dir is graced anyway, but stop here
		}
		if err := os.RemoveAll(filepath.Join(m.cfg.WorkDir, name)); err != nil {
			log.Printf("astrostack: work sweep: %s: %v", name, err)
			continue
		}
		log.Printf("astrostack: work sweep: removed stale run scratch %s", name)
	}
}
