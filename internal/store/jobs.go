package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Job status values.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
	// JobPaused is a non-terminal rest state: the run stopped at a safe boundary (manually, or on a
	// transient S3 error) and can be resumed from its `resume` checkpoint. Not "running" (so a restart
	// won't force-fail it) and not terminal (it isn't done).
	JobPaused = "paused"
)

// Job is a unit of background work (a pipeline or video run).
type Job struct {
	ID           int64           `json:"id" db:"id"`
	SessionID    int64           `json:"session_id" db:"session_id"`
	Kind         string          `json:"kind" db:"kind"`
	Status       string          `json:"status" db:"status"`
	Progress     int             `json:"progress" db:"progress"`
	CurrentStep  string          `json:"current_step" db:"current_step"`
	LogTail      string          `json:"log_tail" db:"log_tail"`
	Error        string          `json:"error" db:"error"`
	Params       json.RawMessage `json:"params" db:"params"`
	Result       json.RawMessage `json:"result" db:"result"`
	Resume       json.RawMessage `json:"resume" db:"resume"` // pause/resume checkpoint ({} when not paused)
	StartedAtMs  int64           `json:"started_at_ms" db:"started_at_ms"`
	FinishedAtMs int64           `json:"finished_at_ms" db:"finished_at_ms"`
	SeriesID     int64           `json:"series_id" db:"series_id"` // agent improvement series (0 = none)
	CreatedAt    int64           `json:"created_at" db:"created_at"`
	UpdatedAt    int64           `json:"updated_at" db:"updated_at"`
}

// CreateSession inserts a bare session and returns its id.
func (s *Store) CreateSession(ctx context.Context, rootPath, object string) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO sessions(root_path, object, created_at, updated_at)
		 VALUES($1,$2,$3,$3) RETURNING id`, rootPath, object, now).Scan(&id)
	return id, err
}

// CreateJob inserts a queued job and returns its id.
func (s *Store) CreateJob(ctx context.Context, sessionID int64, kind string, params json.RawMessage) (int64, error) {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO jobs(session_id, kind, status, params, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$5) RETURNING id`,
		sessionID, kind, JobQueued, params, now).Scan(&id)
	return id, err
}

// SetJobRunning marks a job running and stamps started_at. It also CLEARS any error — a running job has no
// error, and on resume the paused state stored its (benign) pause reason there (SetJobPaused), which would
// otherwise linger as a stale red failure banner once the job is running again.
func (s *Store) SetJobRunning(ctx context.Context, id int64) error {
	now := nowMs()
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, started_at_ms=$3, error='', updated_at=$3 WHERE id=$1`,
		id, JobRunning, now)
	return err
}

// UpdateJobProgress updates the live progress fields of a running job.
func (s *Store) UpdateJobProgress(ctx context.Context, id int64, progress int, step, logTail string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET progress=$2, current_step=$3, log_tail=$4, updated_at=$5 WHERE id=$1`,
		id, progress, step, logTail, nowMs())
	return err
}

// FinishJob records the terminal status, result and error of a job. It clears any resume checkpoint —
// a finished job is never resumed.
func (s *Store) FinishJob(ctx context.Context, id int64, status string, result json.RawMessage, errMsg string) error {
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	now := nowMs()
	progress := 100
	if status == JobFailed || status == JobCancelled {
		progress = 0
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, result=$3, error=$4, progress=COALESCE(NULLIF($5,0), progress),
		    resume='{}', finished_at_ms=$6, updated_at=$6 WHERE id=$1`,
		id, status, result, errMsg, progress, now)
	return err
}

// SetJobPaused parks a job in the resumable "paused" state, storing its resume checkpoint and (optionally)
// the result produced so far. Unlike FinishJob it PRESERVES progress/current_step so the UI keeps showing
// how far the run got, and it does not stamp finished_at_ms (the job isn't finished). A nil result leaves
// the existing result untouched (e.g. a compute-phase pause has no final result yet).
func (s *Store) SetJobPaused(ctx context.Context, id int64, resume, result json.RawMessage, reason string) error {
	if len(resume) == 0 {
		resume = json.RawMessage("{}")
	}
	now := nowMs()
	if result == nil { // leave the stored result untouched (e.g. a compute-phase pause has no final result)
		_, err := s.pool.Exec(ctx,
			`UPDATE jobs SET status=$2, resume=$3, error=$4, updated_at=$5 WHERE id=$1`,
			id, JobPaused, resume, reason, now)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE jobs SET status=$2, resume=$3, error=$4, result=$5, updated_at=$6 WHERE id=$1`,
		id, JobPaused, resume, reason, result, now)
	return err
}

// RequeueRunningJobs returns every job orphaned by a restart to the QUEUE instead of failing it, and
// reports how many were requeued and how many were given up on.
//
// Jobs run in an in-process worker pool that does not survive a restart, so any row still "running" at
// boot has no live worker. Failing them was the old behaviour and it is expensive in a way that is easy
// to underestimate: on one afternoon seven runs died this way, each losing 15 to 45 minutes of stacking
// and stranding 30 to 70 GB of scratch, because a rebuild happens to be a normal part of working on the
// engine. Requeued instead, a rebuild costs a restart rather than the run — and the content-keyed
// caches (background extraction, AI denoise, colour calibration) mean the retry can skip whole stages
// whose inputs come back byte-identical.
//
// The restart count lives in the resume checkpoint, which already exists for pause/resume and needs no
// migration. It is what keeps this from becoming a loop: a job that takes the engine down WITH it would
// otherwise be requeued forever, taking the engine down again on every boot. Past maxRestarts it fails
// for good, saying how many restarts it saw.
func (s *Store) RequeueRunningJobs(ctx context.Context, maxRestarts int, requeueNote, giveUpNote string) (requeued, abandoned int64, err error) {
	now := nowMs()
	// Every CASE reads `resume` as it was BEFORE this statement's SET, so all four branches agree on
	// the same restart count. The ::bigint casts are load-bearing: without them Postgres infers $7's
	// type from the `THEN 0` literal beside it, decides int4, and rejects a millisecond timestamp as
	// "greater than maximum value for int4" — which would fail the reconcile on every boot.
	rows, err := s.pool.Query(ctx, `
		UPDATE jobs SET
		  resume         = jsonb_set(resume, '{restarts}', to_jsonb(COALESCE((resume->>'restarts')::int, 0) + 1), true),
		  status         = CASE WHEN COALESCE((resume->>'restarts')::int, 0) < $2 THEN $3 ELSE $4 END,
		  error          = CASE WHEN COALESCE((resume->>'restarts')::int, 0) < $2 THEN $5 ELSE $6 END,
		  progress       = 0,
		  started_at_ms  = 0,
		  finished_at_ms = CASE WHEN COALESCE((resume->>'restarts')::int, 0) < $2 THEN 0::bigint ELSE $7::bigint END,
		  updated_at     = $7::bigint
		WHERE status = $1
		RETURNING status`,
		JobRunning, maxRestarts, JobQueued, JobFailed, requeueNote, giveUpNote, now)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return requeued, abandoned, err
		}
		if status == JobQueued {
			requeued++
		} else {
			abandoned++
		}
	}
	return requeued, abandoned, rows.Err()
}

// GetJob returns one job by id.
func (s *Store) GetJob(ctx context.Context, id int64) (*Job, error) {
	rows, err := s.pool.Query(ctx, jobSelect+` WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	job, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[Job])
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// ListJobs returns jobs newest first (id desc = submission/date order), paginated by limit + offset so the
// Tasks page never loads the whole history at once.
func (s *Store) ListJobs(ctx context.Context, limit, offset int) ([]Job, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.pool.Query(ctx, jobSelect+` ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Job])
}

// ListPausedJobs returns every job in the resumable paused state, oldest first — the input to the
// auto-resume sweep, which restarts the error-caused ones whose backoff has elapsed.
func (s *Store) ListPausedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.pool.Query(ctx, jobSelect+` WHERE status=$1 ORDER BY id ASC`, JobPaused)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Job])
}

// ListQueuedJobs returns every job still in the queued state, oldest first. A server restart reloads
// these to re-dispatch them: a queued job is scheduled by pushing its id onto an in-process lane channel,
// which does not survive a restart, so without a reload the DB row would sit queued with no worker to run it.
func (s *Store) ListQueuedJobs(ctx context.Context) ([]Job, error) {
	rows, err := s.pool.Query(ctx, jobSelect+` WHERE status=$1 ORDER BY id ASC`, JobQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Job])
}

// CountJobs returns the total number of jobs (for the Tasks page's pagination / "load more").
func (s *Store) CountJobs(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&n)
	return n, err
}

const jobSelect = `SELECT id, session_id, kind, status, progress, current_step, log_tail, error,
	params, result, resume, started_at_ms, finished_at_ms, series_id, created_at, updated_at FROM jobs`
