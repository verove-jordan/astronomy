package job

// Full-resolution stage exports for a finished job: list what the run preserved, and render one of
// those stages to PNG or TIFF at native resolution. The pipeline owns the rules about WHICH stages
// can honestly be re-rendered (see pipeline/stageexport.go); this only resolves a job id to its run
// directory and lends the Siril runner.

import (
	"context"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/store"
)

// runDirOf resolves a finished job to the run directory its result points at.
func (m *Manager) runDirOf(ctx context.Context, jobID int64) (string, error) {
	j, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return "", err
	}
	if j.Status == store.JobQueued || j.Status == store.JobRunning {
		return "", fmt.Errorf("job %d is still %s", jobID, j.Status)
	}
	dir := runDirFromResult(j.Result)
	if dir == "" {
		return "", fmt.Errorf("job %d has no completed run", jobID)
	}
	return dir, nil
}

// StageArtifacts lists the exportable full-resolution stages of a finished job's run.
func (m *Manager) StageArtifacts(ctx context.Context, jobID int64) ([]pipeline.StageArtifact, error) {
	dir, err := m.runDirOf(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return pipeline.StageArtifacts(dir), nil
}

// ExportStage renders one stage of a finished job at full resolution and returns the written file.
func (m *Manager) ExportStage(ctx context.Context, jobID int64, key, format string) (string, error) {
	dir, err := m.runDirOf(ctx, jobID)
	if err != nil {
		return "", err
	}
	return pipeline.ExportStage(ctx, m.runner, dir, key, format)
}
