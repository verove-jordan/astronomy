package store

// Live-DB test for the restart reconcile. It needs the real Postgres (the SQL does the counting in a
// single UPDATE with jsonb_set, which no fake can stand in for), so it is skipped unless a DSN is given:
//
//	ASTRO_TEST_DSN='postgres://astro:astro@localhost:5432/astrostack' go test ./internal/store -run Requeue

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func liveStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ASTRO_TEST_DSN")
	if dsn == "" {
		t.Skip("set ASTRO_TEST_DSN to run the live-DB store tests")
	}
	s, err := New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(s.Close)
	return s
}

// runningJob creates a job and puts it in the running state, as a worker would.
func runningJob(t *testing.T, s *Store, ctx context.Context) int64 {
	t.Helper()
	sid, err := s.CreateSession(ctx, "/tmp/requeue-test", "requeue-test")
	require.NoError(t, err)
	id, err := s.CreateJob(ctx, sid, "deepsky", json.RawMessage(`{"mode":"deepsky"}`))
	require.NoError(t, err)
	require.NoError(t, s.SetJobRunning(ctx, id))
	return id
}

func restartCount(t *testing.T, j *Job) int {
	t.Helper()
	var cp struct {
		Restarts int `json:"restarts"`
	}
	require.NoError(t, json.Unmarshal(j.Resume, &cp))
	return cp.Restarts
}

// A rebuild must cost the restart, not the run: an orphaned job goes back on the QUEUE, where
// redispatchQueued picks it up, instead of being failed after 45 minutes of stacking.
func TestRequeueRunningJobs_PutsAnOrphanBackOnTheQueue(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	id := runningJob(t, s, ctx)

	requeued, abandoned, err := s.RequeueRunningJobs(ctx, 3, "requeued", "gave up")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, requeued, int64(1))
	assert.Equal(t, int64(0), abandoned)

	j, err := s.GetJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, JobQueued, j.Status)
	assert.Equal(t, "requeued", j.Error, "the reason stays visible in the UI")
	assert.Equal(t, 0, j.Progress, "a requeued run starts from the beginning")
	assert.Equal(t, int64(0), j.FinishedAtMs, "and is not a finished job")
	assert.Equal(t, 1, restartCount(t, j))
}

// The bound is what stops a job that takes the engine down WITH it from being handed back forever.
func TestRequeueRunningJobs_GivesUpAfterTheBound(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	id := runningJob(t, s, ctx)

	const maxRestarts = 2
	for i := 1; i <= maxRestarts; i++ {
		_, _, err := s.RequeueRunningJobs(ctx, maxRestarts, "requeued", "gave up")
		require.NoError(t, err)
		j, err := s.GetJob(ctx, id)
		require.NoError(t, err)
		require.Equal(t, JobQueued, j.Status, "still under the bound after %d restart(s)", i)
		require.Equal(t, i, restartCount(t, j))
		require.NoError(t, s.SetJobRunning(ctx, id)) // a worker picks it up again
	}

	_, abandoned, err := s.RequeueRunningJobs(ctx, maxRestarts, "requeued", "gave up")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, abandoned, int64(1))

	j, err := s.GetJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, JobFailed, j.Status)
	assert.Equal(t, "gave up", j.Error)
	assert.NotZero(t, j.FinishedAtMs, "a job given up on IS finished")
	assert.Equal(t, maxRestarts+1, restartCount(t, j))
}

// Only running jobs are touched — a queued, paused or finished job is none of this reconcile's business.
func TestRequeueRunningJobs_LeavesOtherStatesAlone(t *testing.T) {
	s, ctx := liveStore(t), context.Background()
	sid, err := s.CreateSession(ctx, "/tmp/requeue-test", "requeue-test")
	require.NoError(t, err)
	queued, err := s.CreateJob(ctx, sid, "deepsky", json.RawMessage(`{"mode":"deepsky"}`))
	require.NoError(t, err)
	paused, err := s.CreateJob(ctx, sid, "deepsky", json.RawMessage(`{"mode":"deepsky"}`))
	require.NoError(t, err)
	require.NoError(t, s.SetJobPaused(ctx, paused, json.RawMessage(`{"phase":"compute"}`), nil, "manual"))

	_, _, err = s.RequeueRunningJobs(ctx, 3, "requeued", "gave up")
	require.NoError(t, err)

	q, err := s.GetJob(ctx, queued)
	require.NoError(t, err)
	assert.Equal(t, JobQueued, q.Status)
	assert.Empty(t, q.Error, "an untouched queued job keeps its empty error")
	assert.Equal(t, 0, restartCount(t, q), "and gains no restart count")

	p, err := s.GetJob(ctx, paused)
	require.NoError(t, err)
	assert.Equal(t, JobPaused, p.Status, "a paused job is resumed deliberately, not by a reconcile")
	assert.Equal(t, "manual", p.Error)
}
