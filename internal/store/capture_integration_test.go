package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A real Postgres, because the bug this protects against is one no fake can have.
//
// UpdateCaptureSession's `ended_at = CASE WHEN $5 <> 0 THEN $5 ELSE ended_at END` made Postgres infer
// the parameter as int4 from the untyped literal it was compared against, so every write carrying a
// real millisecond timestamp failed with "1788485806470 is greater than maximum value for int4".
// Only the TERMINAL write carries one — the per-frame writes pass 0 and went through — so frames_done
// advanced normally while status stayed "running" forever. Every completed, aborted and failed
// session in the logbook was a phantom. A mocked driver would have accepted the int64 happily and
// proved nothing.

// testStore connects to the development database, or skips. It is deliberately not a container of
// its own: the Compose Postgres is already the one this app runs against, and a test that passes
// against a different server than production uses is the kind of test that misses type inference.
func testStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://astro:astro@localhost:5432/astrostack?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := New(ctx, dsn)
	if err != nil {
		t.Skipf("no development database reachable (%v) — run `just up`", err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestUpdateCaptureSession_ClosesASessionWithAMillisecondTimestamp(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	id, err := st.CreateCaptureSession(ctx, CaptureSession{
		Object: "integration-test", Root: t.TempDir(), TileIndex: -1,
		Status: "running", TotalFrames: 3, StartedAt: nowMs(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM capture_sessions WHERE id=$1`, id)
	})

	// A per-frame write: not terminal, so ended_at stays 0 and the row keeps running.
	require.NoError(t, st.UpdateCaptureSession(ctx, id, "running", []byte(`{"status":"running"}`), 2, false))
	row, err := st.GetCaptureSession(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "running", row.Status)
	assert.Equal(t, 2, row.FramesDone)
	assert.Zero(t, row.EndedAt)

	// The terminal write. This is the one that used to fail.
	require.NoError(t, st.UpdateCaptureSession(ctx, id, "completed", []byte(`{"status":"completed"}`), 3, true),
		"a terminal write carries a millisecond ended_at and must not overflow int4")

	row, err = st.GetCaptureSession(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "completed", row.Status)
	assert.Equal(t, 3, row.FramesDone)
	assert.Greater(t, row.EndedAt, int64(1_700_000_000_000),
		"ended_at must be a real millisecond timestamp, which is what overflowed")
}

// The whole request rides with the session so a night can be finished with the optics it was
// actually shot at. Round-tripping it through JSONB is the part worth proving.
func TestCreateCaptureSession_KeepsTheRequestItWasStartedWith(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	const request = `{"focal_mm":250,"telescope":"RedCat 51","ra_deg":10.684,"dither_radius_px":12}`

	id, err := st.CreateCaptureSession(ctx, CaptureSession{
		Object: "integration-test", Root: t.TempDir(), TileIndex: -1,
		Status: "running", StartedAt: nowMs(), Request: []byte(request),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM capture_sessions WHERE id=$1`, id)
	})

	row, err := st.GetCaptureSession(ctx, id)
	require.NoError(t, err)
	assert.JSONEq(t, request, string(row.Request))
}
