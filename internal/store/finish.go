package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// FinishIteration is one supervised finish render + decision. params/metrics/defects are JSON blobs
// (the tuned params, the measured finish metrics, and the model's diagnosed defects); tier is the
// pipeline re-entry level the iteration used (A = composite, B = finish prep, C = re-stack).
// png_path + preset (the FULL working preset, versioned {"v":1,...}) make the table the supervisor's
// cross-run MEMORY: later attempts warm-start from the best prior preset and diff against it.
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
	PngPath       string          `json:"png_path" db:"png_path"`
	Preset        json.RawMessage `json:"preset" db:"preset"`
	CreatedAt     int64           `json:"created_at" db:"created_at"`
	UpdatedAt     int64           `json:"updated_at" db:"updated_at"`
}

// CreateFinishIteration inserts one supervised finish iteration and returns its id. The []byte
// params/metrics/defects/preset are sent as jsonb. Satisfies pipeline.FinishIterStore.
func (s *Store) CreateFinishIteration(ctx context.Context, jobID int64, iter int, tier string, params, metrics, defects []byte, detScore, modelScore, combined float64, reasoning, pngPath string, preset []byte) (int64, error) {
	if len(params) == 0 {
		params = []byte("{}")
	}
	if len(metrics) == 0 {
		metrics = []byte("{}")
	}
	if len(defects) == 0 {
		defects = []byte("[]")
	}
	if len(preset) == 0 {
		preset = []byte("{}")
	}
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO finish_iterations(job_id, iteration, tier, params, metrics, defects, det_score,
		    model_score, combined_score, reasoning, chosen, png_path, preset, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,false,$11,$12,$13,$13) RETURNING id`,
		jobID, iter, tier, json.RawMessage(params), json.RawMessage(metrics), json.RawMessage(defects),
		detScore, modelScore, combined, reasoning, pngPath, json.RawMessage(preset), now).Scan(&id)
	return id, err
}

// MarkFinishIterationChosen flags the winning iteration. Satisfies pipeline.FinishIterStore.
func (s *Store) MarkFinishIterationChosen(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE finish_iterations SET chosen=true, updated_at=$2 WHERE id=$1`, id, nowMs())
	return err
}

// ListFinishIterations returns a job's iterations in order (for history and warm-start priors).
func (s *Store) ListFinishIterations(ctx context.Context, jobID int64) ([]FinishIteration, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, job_id, iteration, tier, params, metrics, defects, det_score, model_score,
		    combined_score, reasoning, chosen, png_path, preset, created_at, updated_at
		 FROM finish_iterations WHERE job_id=$1 ORDER BY iteration`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[FinishIteration])
}

// BestFinishIterations returns the top prior iterations for a target (object + job kind) across ALL
// jobs, best combined score first — the warm-start priors a new supervised run seeds from. Only
// decent passes qualify (det ≥ minDet keeps garbage renders from poisoning the seed); rows are
// re-clamped by the pipeline on read, so stale presets are harmless. Satisfies
// pipeline.FinishPriorStore.
func (s *Store) BestFinishIterations(ctx context.Context, object, kind string, minDet float64, limit int) ([]FinishIteration, error) {
	if limit <= 0 {
		limit = 3
	}
	rows, err := s.pool.Query(ctx,
		`SELECT f.id, f.job_id, f.iteration, f.tier, f.params, f.metrics, f.defects, f.det_score,
		    f.model_score, f.combined_score, f.reasoning, f.chosen, f.png_path, f.preset,
		    f.created_at, f.updated_at
		 FROM finish_iterations f
		 JOIN jobs j ON j.id = f.job_id
		 WHERE j.kind = $2 AND j.result->>'object' = $1 AND f.det_score >= $3 AND f.preset <> '{}'::jsonb
		 ORDER BY f.combined_score DESC, f.id DESC
		 LIMIT $4`, object, kind, minDet, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[FinishIteration])
}
