package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// AgentSeries is one durable improvement campaign over a target: each attempt is a normal job
// linked by jobs.series_id, so the cross-job iteration history is a join, not new plumbing.
type AgentSeries struct {
	ID           int64   `json:"id" db:"id"`
	Object       string  `json:"object" db:"object"`
	Kind         string  `json:"kind" db:"kind"`
	InputPath    string  `json:"input_path" db:"input_path"`
	Goal         string  `json:"goal" db:"goal"`
	Status       string  `json:"status" db:"status"` // active | done | stopped
	AutoContinue bool    `json:"auto_continue" db:"auto_continue"`
	MaxAttempts  int     `json:"max_attempts" db:"max_attempts"`
	TargetScore  float64 `json:"target_score" db:"target_score"`
	BestJobID    int64   `json:"best_job_id" db:"best_job_id"`
	BestScore    float64 `json:"best_score" db:"best_score"`
	CreatedAt    int64   `json:"created_at" db:"created_at"`
	UpdatedAt    int64   `json:"updated_at" db:"updated_at"`
}

// CreateSeries inserts a new improvement series and returns its id.
func (s *Store) CreateSeries(ctx context.Context, sr AgentSeries) (int64, error) {
	if sr.Status == "" {
		sr.Status = "active"
	}
	if sr.MaxAttempts <= 0 {
		sr.MaxAttempts = 3
	}
	if sr.TargetScore <= 0 {
		sr.TargetScore = 8
	}
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO agent_series(object, kind, input_path, goal, status, auto_continue, max_attempts,
		    target_score, best_job_id, best_score, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,0,0,$9,$9) RETURNING id`,
		sr.Object, sr.Kind, sr.InputPath, sr.Goal, sr.Status, sr.AutoContinue, sr.MaxAttempts,
		sr.TargetScore, now).Scan(&id)
	return id, err
}

// GetSeries returns one series.
func (s *Store) GetSeries(ctx context.Context, id int64) (AgentSeries, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, object, kind, input_path, goal, status, auto_continue,
	    max_attempts, target_score, best_job_id, best_score, created_at, updated_at
	 FROM agent_series WHERE id=$1`, id)
	if err != nil {
		return AgentSeries{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[AgentSeries])
}

// ListSeries returns recent series, newest first.
func (s *Store) ListSeries(ctx context.Context, limit int) ([]AgentSeries, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, object, kind, input_path, goal, status, auto_continue,
	    max_attempts, target_score, best_job_id, best_score, created_at, updated_at
	 FROM agent_series ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[AgentSeries])
}

// SeriesJobs returns the series' attempts (jobs), oldest first.
func (s *Store) SeriesJobs(ctx context.Context, seriesID int64) ([]Job, error) {
	rows, err := s.pool.Query(ctx, jobSelect+` WHERE series_id=$1 ORDER BY id`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Job])
}

// SetJobSeries links a job to a series.
func (s *Store) SetJobSeries(ctx context.Context, jobID, seriesID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE jobs SET series_id=$2, updated_at=$3 WHERE id=$1`,
		jobID, seriesID, nowMs())
	return err
}

// CountSeriesAttempts counts the series' jobs.
func (s *Store) CountSeriesAttempts(ctx context.Context, seriesID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs WHERE series_id=$1`, seriesID).Scan(&n)
	return n, err
}

// UpdateSeriesBest records a new best attempt (job + score).
func (s *Store) UpdateSeriesBest(ctx context.Context, id, bestJobID int64, bestScore float64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agent_series SET best_job_id=$2, best_score=$3, updated_at=$4 WHERE id=$1`,
		id, bestJobID, bestScore, nowMs())
	return err
}

// SetSeriesStatus moves a series between active/done/stopped.
func (s *Store) SetSeriesStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agent_series SET status=$2, updated_at=$3 WHERE id=$1`,
		id, status, nowMs())
	return err
}
