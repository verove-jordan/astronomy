package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/store"
)

// mastersResult is the persisted result of a masters-only build job (kind "masters").
type mastersResult struct {
	Sets     int      `json:"sets"`   // calibration sets found in the selection
	Built    int      `json:"built"`  // masters newly stacked and added to the library
	Reused   int      `json:"reused"` // sets already covered by a library master (or failed — see warnings)
	Warnings []string `json:"warnings,omitempty"`
}

// runBuildMasters executes a masters-only job: stack the selection's dark/flat/bias frames into master
// calibration files and add them to the persistent library — no lights, no image. Intercepted in
// execute() before mode parsing (like Transfer/Backup), so it reuses the whole job progress/SSE stack.
func (m *Manager) runBuildMasters(ctx context.Context, id int64, p RunRequest) (any, error) {
	dirs := p.Paths
	if len(dirs) == 0 {
		dirs = []string{p.Path}
	}
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 0, Step: "scanning calibration frames"})
	inv, err := inspect.ScanMany(ctx, dirs, inspect.DefaultScanOptions())
	if err != nil {
		return nil, fmt.Errorf("scan calibration folders: %w", err)
	}
	sets := 0
	for _, ft := range []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat} {
		sets += len(inv.SetsOfType(ft))
	}
	if sets == 0 {
		return nil, fmt.Errorf("no calibration frames (darks/flats/bias) found in the selection")
	}

	// Library size before the build tells how many masters this job actually added.
	before, err := m.store.ListMasters(ctx)
	if err != nil {
		return nil, fmt.Errorf("read calibration library: %w", err)
	}

	workRun := filepath.Join(m.cfg.WorkDir, fmt.Sprintf("masters_%d", id))
	defer func() { _ = os.RemoveAll(workRun) }()

	step := fmt.Sprintf("building %d master calibration set(s)", sets)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 1, Step: step})
	masters, warnings, err := calib.BuildOrReuseMasters(ctx, m.runner, inv, m.store, m.cfg.LibraryDir,
		workRun, m.mastersProgress(ctx, id, step))
	if err != nil {
		return nil, err
	}

	built := len(masters) - len(before)
	if built < 0 {
		built = 0
	}
	// Reused approximates sets minus built; a set that failed to stack lands here too — its warning
	// line says exactly why, so the summary stays honest without brittle warning-string matching.
	reused := sets - built
	if reused < 0 {
		reused = 0
	}
	for _, w := range warnings {
		m.publish(Event{JobID: id, Status: store.JobRunning, Line: "⚠ " + w, Ts: time.Now().UnixMilli()})
	}
	summary := fmt.Sprintf("built %d new master(s), reused %d — available on the Library page", built, reused)
	m.publish(Event{JobID: id, Status: store.JobRunning, Progress: 100, Step: step, Line: summary,
		Ts: time.Now().UnixMilli()})
	return mastersResult{Sets: sets, Built: built, Reused: reused, Warnings: warnings}, nil
}

// mastersProgress adapts Siril stack output to job events: every line streams to the live log, and the
// per-set percentage drives the bar monotonically (each set's stack sweeps 0→100; the max seen so far
// keeps the global bar from jumping backwards between sets) with the persisted progress throttled to
// percent changes.
func (m *Manager) mastersProgress(ctx context.Context, id int64, step string) func(siril.Progress) {
	last := 0
	return func(pr siril.Progress) {
		ev := Event{JobID: id, Status: store.JobRunning, Step: step, Ts: time.Now().UnixMilli()}
		if pr.Line != "" {
			ev.Line = pr.Line
		}
		if pr.Percent > last {
			last = pr.Percent
			_ = m.store.UpdateJobProgress(ctx, id, last, step, pr.Line)
		}
		ev.Progress = last
		m.publish(ev)
	}
}
