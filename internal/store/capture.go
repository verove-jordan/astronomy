package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Capture bookkeeping: reusable auto-run sequences, the sessions shot from them, and every frame
// written. Sessions carry the target/tile/night context the device server knows nothing about.

// CaptureSequence is a saved auto-run plan.
type CaptureSequence struct {
	ID        int64  `json:"id" db:"id"`
	Name      string `json:"name" db:"name"`
	Payload   []byte `json:"payload" db:"payload"` // capture.Sequence, opaque here
	Favorite  bool   `json:"favorite" db:"favorite"`
	CreatedAt int64  `json:"created_at" db:"created_at"`
	UpdatedAt int64  `json:"updated_at" db:"updated_at"`
}

const captureSequenceCols = `id,name,payload,favorite,created_at,updated_at`

func (s *Store) ListCaptureSequences(ctx context.Context) ([]CaptureSequence, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureSequenceCols+` FROM capture_sequences ORDER BY favorite DESC, LOWER(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureSequence])
}

// SaveCaptureSequence upserts by name, so re-saving a tweaked plan overwrites rather than
// duplicating — the same behaviour presets and equipment setups have.
func (s *Store) SaveCaptureSequence(ctx context.Context, name string, payload []byte, favorite bool) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO capture_sequences(name,payload,favorite,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$4)
		 ON CONFLICT (LOWER(name)) DO UPDATE SET
		   name=EXCLUDED.name, payload=EXCLUDED.payload, favorite=EXCLUDED.favorite, updated_at=$4
		 RETURNING id`,
		name, payload, favorite, now).Scan(&id)
	return id, err
}

func (s *Store) DeleteCaptureSequence(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM capture_sequences WHERE id=$1`, id)
	return err
}

// CaptureSession is one night's run of a sequence.
type CaptureSession struct {
	ID           int64  `json:"id" db:"id"`
	Object       string `json:"object" db:"object"`
	Root         string `json:"root" db:"root"`
	Panel        string `json:"panel" db:"panel"`
	MosaicPlanID int64  `json:"mosaic_plan_id" db:"mosaic_plan_id"`
	TileIndex    int    `json:"tile_index" db:"tile_index"`
	Sequence     []byte `json:"sequence" db:"sequence"`
	Status       string `json:"status" db:"status"`
	Progress     []byte `json:"progress" db:"progress"`
	TotalFrames  int    `json:"total_frames" db:"total_frames"`
	FramesDone   int    `json:"frames_done" db:"frames_done"`
	StartedAt    int64  `json:"started_at" db:"started_at"`
	EndedAt      int64  `json:"ended_at" db:"ended_at"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
	UpdatedAt    int64  `json:"updated_at" db:"updated_at"`
}

const captureSessionCols = `id,object,root,panel,mosaic_plan_id,tile_index,sequence,status,progress,` +
	`total_frames,frames_done,started_at,ended_at,created_at,updated_at`

func (s *Store) ListCaptureSessions(ctx context.Context, limit int) ([]CaptureSession, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureSessionCols+` FROM capture_sessions ORDER BY started_at DESC, id DESC LIMIT $1`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureSession])
}

func (s *Store) GetCaptureSession(ctx context.Context, id int64) (CaptureSession, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+captureSessionCols+` FROM capture_sessions WHERE id=$1`, id)
	if err != nil {
		return CaptureSession{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CaptureSession])
}

// CreateCaptureSession opens a session row at the moment capture starts, so a crash mid-night still
// leaves a record of what was being shot.
func (s *Store) CreateCaptureSession(ctx context.Context, sess CaptureSession) (int64, error) {
	now := nowMs()
	if sess.StartedAt == 0 {
		sess.StartedAt = now
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO capture_sessions(object,root,panel,mosaic_plan_id,tile_index,sequence,status,
		   progress,total_frames,frames_done,started_at,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,$11,$11) RETURNING id`,
		sess.Object, sess.Root, sess.Panel, sess.MosaicPlanID, sess.TileIndex,
		jsonOrEmpty(sess.Sequence), sess.Status, jsonOrEmpty(sess.Progress),
		sess.TotalFrames, sess.StartedAt, now).Scan(&id)
	return id, err
}

// UpdateCaptureSession records the live progress and, on a terminal status, the end time.
func (s *Store) UpdateCaptureSession(ctx context.Context, id int64, status string, progress []byte, framesDone int, ended bool) error {
	now := nowMs()
	endedAt := int64(0)
	if ended {
		endedAt = now
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE capture_sessions
		 SET status=$2, progress=$3, frames_done=$4,
		     ended_at = CASE WHEN $5 <> 0 THEN $5 ELSE ended_at END,
		     updated_at=$6
		 WHERE id=$1`,
		id, status, jsonOrEmpty(progress), framesDone, endedAt, now)
	return err
}

// CaptureFrame is one written exposure.
type CaptureFrame struct {
	ID          int64  `json:"id" db:"id"`
	SessionID   int64  `json:"session_id" db:"session_id"`
	Path        string `json:"path" db:"path"`
	Filter      string `json:"filter" db:"filter"`
	FrameType   string `json:"frame_type" db:"frame_type"`
	ExposureUs  int64  `json:"exposure_us" db:"exposure_us"`
	Gain        int64  `json:"gain" db:"gain"`
	FrameOffset int64  `json:"frame_offset" db:"frame_offset"`
	Bin         int    `json:"bin" db:"bin"`
	TempMilliC  int    `json:"temp_milli_c" db:"temp_milli_c"`
	Panel       string `json:"panel" db:"panel"`
	SequenceNo  int    `json:"sequence_no" db:"sequence_no"`
	StartedAt   int64  `json:"started_at" db:"started_at"`
	CreatedAt   int64  `json:"created_at" db:"created_at"`
}

const captureFrameCols = `id,session_id,path,filter,frame_type,exposure_us,gain,frame_offset,bin,` +
	`temp_milli_c,panel,sequence_no,started_at,created_at`

func (s *Store) RecordCaptureFrame(ctx context.Context, f CaptureFrame) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO capture_frames(session_id,path,filter,frame_type,exposure_us,gain,frame_offset,
		   bin,temp_milli_c,panel,sequence_no,started_at,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		f.SessionID, f.Path, f.Filter, f.FrameType, f.ExposureUs, f.Gain, f.FrameOffset,
		f.Bin, f.TempMilliC, f.Panel, f.SequenceNo, f.StartedAt, nowMs())
	return err
}

func (s *Store) ListCaptureFrames(ctx context.Context, sessionID int64) ([]CaptureFrame, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureFrameCols+` FROM capture_frames WHERE session_id=$1 ORDER BY sequence_no`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureFrame])
}

// jsonOrEmpty keeps a nil JSONB payload from failing the NOT NULL constraint.
func jsonOrEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// InterruptOrphanedCaptureSessions marks live-looking sessions as interrupted, and reports how many.
//
// A capture session is owned by the in-process runner, so nothing can be mid-exposure across a
// restart. Any row still reading "running" or "paused" at startup was orphaned — the engine was
// stopped, hot-reloaded by `air`, or crashed while a run was in flight. Left alone they linger as
// phantom active runs in the sessions list forever, which is worse than an honest "interrupted":
// it implies frames are still arriving when nothing is driving the camera.
func (s *Store) InterruptOrphanedCaptureSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE capture_sessions
		    SET status='interrupted', ended_at=$1, updated_at=$1
		  WHERE status IN ('running','paused')`, nowMs())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
