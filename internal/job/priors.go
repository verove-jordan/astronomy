package job

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/store"
)

// finishPriors adapts the store's finish_iterations rows to the pipeline's DB-free warm-start
// interface (pipeline.FinishPriorStore) — the supervisor's cross-run memory.
type finishPriors struct{ st *store.Store }

func (f finishPriors) BestFinishIterations(ctx context.Context, object, kind string, minDet float64, limit int) ([]pipeline.PriorIteration, error) {
	rows, err := f.st.BestFinishIterations(ctx, object, kind, minDet, limit)
	if err != nil {
		return nil, err
	}
	out := make([]pipeline.PriorIteration, len(rows))
	for i, r := range rows {
		out[i] = pipeline.PriorIteration{
			JobID: r.JobID, Tier: r.Tier, Combined: r.CombinedScore, Det: r.DetScore,
			Reasoning: r.Reasoning, PngPath: r.PngPath, Preset: r.Preset,
		}
	}
	return out, nil
}

// priors returns the manager's warm-start reader (nil-safe for storeless tests).
func (m *Manager) priors() pipeline.FinishPriorStore {
	if m.store == nil {
		return nil
	}
	return finishPriors{m.store}
}
