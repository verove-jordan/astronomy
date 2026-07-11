// Agent improvement series: the cross-run loop. A series is one durable campaign over a target;
// every attempt is a normal job linked by series_id. After a supervised attempt succeeds, the
// policy below decides whether to launch the next attempt unattended (auto_continue) — the
// supervisor's warm start makes each attempt CONTINUE the trajectory instead of starting cold.
package job

import (
	"context"
	"fmt"
	"log"

	"github.com/verove-jordan/astronomy/internal/store"
)

// linkSeries attaches a freshly-created job to its series (best-effort; a miss only loses grouping).
func (m *Manager) linkSeries(ctx context.Context, jobID, seriesID int64) {
	if seriesID == 0 || m.store == nil {
		return
	}
	if err := m.store.SetJobSeries(ctx, jobID, seriesID); err != nil {
		log.Printf("job %d: link series %d: %v", jobID, seriesID, err)
	}
}

// maybeContinueSeries runs after a job finishes successfully: it records the attempt's best score on
// the series and — when auto_continue is on, the target score is unmet and attempts remain — launches
// the next attempt as a warm-started supervised re-run. All soft-fail: a series policy error never
// affects the finished job.
func (m *Manager) maybeContinueSeries(jobID int64, req RunRequest) {
	if req.SeriesID == 0 || m.store == nil {
		return
	}
	ctx := context.Background()
	sr, err := m.store.GetSeries(ctx, req.SeriesID)
	if err != nil {
		log.Printf("series %d: read: %v", req.SeriesID, err)
		return
	}
	best := bestIterationScore(ctx, m.store, jobID)
	if best > sr.BestScore {
		if err := m.store.UpdateSeriesBest(ctx, sr.ID, jobID, best); err != nil {
			log.Printf("series %d: update best: %v", sr.ID, err)
		}
		sr.BestScore = best
	}
	if sr.Status != "active" || !sr.AutoContinue {
		return
	}
	if sr.BestScore >= sr.TargetScore {
		_ = m.store.SetSeriesStatus(ctx, sr.ID, "done")
		m.publish(Event{JobID: jobID, Line: fmt.Sprintf("series %d: target score reached (%.1f ≥ %.1f) — done", sr.ID, sr.BestScore, sr.TargetScore)})
		return
	}
	attempts, err := m.store.CountSeriesAttempts(ctx, sr.ID)
	if err != nil || attempts >= sr.MaxAttempts {
		_ = m.store.SetSeriesStatus(ctx, sr.ID, "done")
		m.publish(Event{JobID: jobID, Line: fmt.Sprintf("series %d: attempt budget spent (%d/%d) — keeping the best", sr.ID, attempts, sr.MaxAttempts)})
		return
	}
	// Next attempt: a full supervised re-run of the same request. The warm start seeds it from the
	// best prior iteration, so the loop explores FROM the best known parameters.
	next := req
	next.Refine = nil
	next.Sequential = false
	next.Supervise = true
	newID, err := m.Enqueue(ctx, next)
	if err != nil {
		log.Printf("series %d: continue: %v", sr.ID, err)
		return
	}
	m.publish(Event{JobID: jobID, Line: fmt.Sprintf("series %d: launching attempt %d/%d (job %d)", sr.ID, attempts+1, sr.MaxAttempts, newID)})
}

// bestIterationScore reads a job's best supervised-iteration combined score (0 when none).
func bestIterationScore(ctx context.Context, st *store.Store, jobID int64) float64 {
	iters, err := st.ListFinishIterations(ctx, jobID)
	if err != nil {
		return 0
	}
	best := 0.0
	for _, it := range iters {
		if it.CombinedScore > best {
			best = it.CombinedScore
		}
	}
	return best
}

// seriesJSON is the API projection of a series (see internal/api/series.go).
type seriesJSON struct {
	store.AgentSeries
	Attempts int `json:"attempts"`
}

// SeriesDetail loads a series with its attempts for the API.
func (m *Manager) SeriesDetail(ctx context.Context, id int64) (store.AgentSeries, []store.Job, error) {
	sr, err := m.store.GetSeries(ctx, id)
	if err != nil {
		return store.AgentSeries{}, nil, err
	}
	jobs, err := m.store.SeriesJobs(ctx, id)
	return sr, jobs, err
}

// CreateSeries opens a new improvement campaign and returns its id.
func (m *Manager) CreateSeries(ctx context.Context, sr store.AgentSeries) (int64, error) {
	if m.store == nil {
		return 0, fmt.Errorf("series need a database")
	}
	return m.store.CreateSeries(ctx, sr)
}

// ListSeries returns recent series with their attempt counts.
func (m *Manager) ListSeries(ctx context.Context, limit int) ([]seriesJSON, error) {
	rows, err := m.store.ListSeries(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]seriesJSON, len(rows))
	for i, sr := range rows {
		n, _ := m.store.CountSeriesAttempts(ctx, sr.ID)
		out[i] = seriesJSON{AgentSeries: sr, Attempts: n}
	}
	return out, nil
}

// SetSeriesStatus flips a series between active/stopped (the UI's Continue/Stop actions).
func (m *Manager) SetSeriesStatus(ctx context.Context, id int64, status string) error {
	if status != "active" && status != "stopped" && status != "done" {
		return fmt.Errorf("invalid series status %q", status)
	}
	return m.store.SetSeriesStatus(ctx, id, status)
}
