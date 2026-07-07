package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/pipeline"
)

// classifyS3Error is the pause-vs-fail rule for a failed S3 transfer phase: a user cancel wins, a transient
// network error pauses (so a flaky endpoint never loses a finished stack), anything else fails.
func TestClassifyS3Error(t *testing.T) {
	tests := []struct {
		name   string
		ctxErr error
		err    error
		want   s3Outcome
	}{
		{"user cancel", context.Canceled, errors.New("whatever"), outcomeCancel},
		{"ctx cancelled wins over transient", context.Canceled, io.ErrUnexpectedEOF, outcomeCancel},
		{"transient network → pause", nil, io.ErrUnexpectedEOF, outcomePause},
		{"transient wrapped → pause", nil, fmt.Errorf("upload results M101: %w", io.ErrUnexpectedEOF), outcomePause},
		{"permanent → fail", nil, errors.New("AccessDenied"), outcomeFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyS3Error(tt.ctxErr, tt.err))
		})
	}
}

// A compute-phase checkpoint (with a run id + out dir) yields a pipeline resume handle so the resumed run
// reuses the output dir; a pull-phase checkpoint (nothing computed yet) yields none.
func TestResumeCheckpoint_PipelineResume(t *testing.T) {
	compute := resumeCheckpoint{Phase: phaseCompute, RunID: "20260101_120000", OutDir: "/out/M101/20260101_120000"}
	got := compute.pipelineResume()
	require.NotNil(t, got)
	assert.Equal(t, &pipeline.ResumeState{RunID: "20260101_120000", OutDir: "/out/M101/20260101_120000"}, got)

	assert.Nil(t, resumeCheckpoint{Phase: phasePull}.pipelineResume(), "pull phase computed nothing → no reuse")
}

// laneFor must route a resumed job to the same worker lane a fresh one would use, so Continue can't land a
// transfer/sequential job in the wrong pool.
func TestLaneFor(t *testing.T) {
	m := &Manager{
		queue:     make(chan int64, 1),
		seqQueue:  make(chan int64, 1),
		xferQueue: make(chan int64, 1),
	}
	assert.Equal(t, m.queue, m.laneFor(RunRequest{}), "default run → main lane")
	assert.Equal(t, m.seqQueue, m.laneFor(RunRequest{Sequential: true}), "sequential run → seq lane")
	assert.Equal(t, m.xferQueue, m.laneFor(RunRequest{Transfer: &TransferRequest{}}), "transfer → xfer lane")
	assert.Equal(t, m.xferQueue, m.laneFor(RunRequest{Backup: &BackupRequest{}}), "backup → xfer lane")
}

// resultBlob keeps a prior result's bytes intact (json.RawMessage round-trips) and maps nil → nil so a
// compute-phase pause leaves the stored result untouched.
func TestResultBlob(t *testing.T) {
	assert.Nil(t, resultBlob(nil))
	raw := json.RawMessage(`{"object":"M101","run_id":"x"}`)
	assert.JSONEq(t, string(raw), string(resultBlob(raw)))
}
