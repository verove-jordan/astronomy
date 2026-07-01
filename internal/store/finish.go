package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// FinishIteration is one supervised finish render + decision (v2). params/metrics/defects are JSON
// blobs (the tuned params, the measured finish metrics, and the model's diagnosed defects); tier is
// the pipeline re-entry level the iteration used (A = composite, B = finish prep, C = re-stack).
type FinishIteration struct {
	ID            int64           `json:"id" db:"id"`
	JobID         int64           `json:"job_id" db:"job_id"`
	Iteration     int             `json:"iteration" db:"iteration"`
	Tier          string          `json:"tier" db:"tier"`
	Params        json.RawMessage `json:"params" db:"params"`
	Metrics       json.RawMessage `json:"metrics" db:"metrics"`
	Defects       json.RawMessage `json:"defects" db:"defects"`
	DetScore      float64         `json:"det_score" db:"det_score"`
	ModelScore    float64         `json:"model_score" db:"model_score"`
	CombinedScore float64         `json:"combined_score" db:"combined_score"`
	Reasoning     string          `json:"reasoning" db:"reasoning"`
	Chosen        bool            `json:"chosen" db:"chosen"`
	CreatedAt     int64           `json:"created_at" db:"created_at"`
	UpdatedAt     int64           `json:"updated_at" db:"updated_at"`
}

// CreateFinishIteration inserts one supervised finish iteration and returns its id. The []byte
// params/metrics/defects are sent as jsonb. Satisfies pipeline.FinishIterStore.
func (s *Store) CreateFinishIteration(ctx context.Context, jobID int64, iter int, tier string, params, metrics, defects []byte, detScore, modelScore, combined float64, reasoning string) (int64, error) {
	if len(params) == 0 {
		params = []byte("{}")
	}
	if len(metrics) == 0 {
		metrics = []byte("{}")
	}
	if len(defects) == 0 {
		defects = []byte("[]")
	}
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO finish_iterations(job_id, iteration, tier, params, metrics, defects, det_score,
		    model_score, combined_score, reasoning, chosen, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,false,$11,$11) RETURNING id`,
		jobID, iter, tier, json.RawMessage(params), json.RawMessage(metrics), json.RawMessage(defects),
		detScore, modelScore, combined, reasoning, now).Scan(&id)
	return id, err
}

// MarkFinishIterationChosen flags the winning iteration. Satisfies pipeline.FinishIterStore.
func (s *Store) MarkFinishIterationChosen(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE finish_iterations SET chosen=true, updated_at=$2 WHERE id=$1`, id, nowMs())
	return err
}

// ListFinishIterations returns a job's iterations in order (for history and future warm-start priors).
func (s *Store) ListFinishIterations(ctx context.Context, jobID int64) ([]FinishIteration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, job_id, iteration, tier, params, metrics, defects, det_score, model_score,
		    combined_score, reasoning, chosen, created_at, updated_at
		 FROM finish_iterations WHERE job_id=$1 ORDER BY iteration`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[FinishIteration])
}
