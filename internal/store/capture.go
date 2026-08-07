package store

import (
	"context"
	"fmt"
	"strings"

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

	// Where the telescope stood, and the rolled-up record of the sky it stood under (skylog.Summary).
	// The summary is denormalized onto the session so the logbook list draws one line per night
	// without joining or aggregating; internal/skylog rewrites it after every sample.
	SiteLat           float64 `json:"site_lat" db:"site_lat"`
	SiteLon           float64 `json:"site_lon" db:"site_lon"`
	SiteElevationM    float64 `json:"site_elevation_m" db:"site_elevation_m"`
	ConditionsSummary []byte  `json:"conditions_summary" db:"conditions_summary"`
}

const captureSessionCols = `id,object,root,panel,mosaic_plan_id,tile_index,sequence,status,progress,` +
	`total_frames,frames_done,started_at,ended_at,created_at,updated_at,` +
	`site_lat,site_lon,site_elevation_m,conditions_summary`

// CaptureSessionFilter narrows the logbook. A zero field means "do not filter on it".
type CaptureSessionFilter struct {
	Limit  int
	Offset int
	Object string // case-insensitive substring, per the house text-search rule
	FromMs int64  // started_at >= FromMs
	ToMs   int64  // started_at <= ToMs
}

func (s *Store) ListCaptureSessions(ctx context.Context, limit int) ([]CaptureSession, error) {
	rows, _, err := s.ListCaptureSessionsFiltered(ctx, CaptureSessionFilter{Limit: limit})
	return rows, err
}

// ListCaptureSessionsFiltered pages the logbook and reports the unpaged total, so the UI can show
// "20 of 137" without a second round trip.
func (s *Store) ListCaptureSessionsFiltered(ctx context.Context, f CaptureSessionFilter) ([]CaptureSession, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	var where []string
	var args []any
	if obj := strings.TrimSpace(f.Object); obj != "" {
		args = append(args, "%"+escapeLike(obj)+"%")
		where = append(where, fmt.Sprintf(`LOWER(object) LIKE LOWER($%d) ESCAPE '\'`, len(args)))
	}
	if f.FromMs > 0 {
		args = append(args, f.FromMs)
		where = append(where, fmt.Sprintf("started_at >= $%d", len(args)))
	}
	if f.ToMs > 0 {
		args = append(args, f.ToMs)
		where = append(where, fmt.Sprintf("started_at <= $%d", len(args)))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM capture_sessions`+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, f.Limit, f.Offset)
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureSessionCols+` FROM capture_sessions`+clause+
			fmt.Sprintf(` ORDER BY started_at DESC, id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[CaptureSession])
	return out, total, err
}

// escapeLike neutralises the LIKE wildcards so a search for "M31_v2" looks for that literal string
// rather than treating the underscore as "any character".
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// SetCaptureSessionConditions stores the rolled-up sky record. Called after every conditions sample,
// so a session that never reaches a clean finish still carries a summary of the hours it did get.
func (s *Store) SetCaptureSessionConditions(ctx context.Context, id int64, summary []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE capture_sessions SET conditions_summary=$2, updated_at=$3 WHERE id=$1`,
		id, jsonOrEmpty(summary), nowMs())
	return err
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
		   progress,total_frames,frames_done,started_at,created_at,updated_at,
		   site_lat,site_lon,site_elevation_m)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,0,$10,$11,$11,$12,$13,$14) RETURNING id`,
		sess.Object, sess.Root, sess.Panel, sess.MosaicPlanID, sess.TileIndex,
		jsonOrEmpty(sess.Sequence), sess.Status, jsonOrEmpty(sess.Progress),
		sess.TotalFrames, sess.StartedAt, now,
		sess.SiteLat, sess.SiteLon, sess.SiteElevationM).Scan(&id)
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

// CaptureFrameStat is one filter/type bucket of a session's frames, aggregated in the database.
//
// This is the tally the logbook actually plans from — "how much L do I still owe?" — and the one a
// later stack is judged by. It comes from capture_frames rather than from the session's cached
// progress counters because only the frame rows know exposure, gain, binning and sensor temperature.
type CaptureFrameStat struct {
	Filter          string `json:"filter" db:"filter"`
	FrameType       string `json:"frame_type" db:"frame_type"`
	Frames          int    `json:"frames" db:"frames"`
	TotalExposureUs int64  `json:"total_exposure_us" db:"total_exposure_us"`
	MinExposureUs   int64  `json:"min_exposure_us" db:"min_exposure_us"`
	MaxExposureUs   int64  `json:"max_exposure_us" db:"max_exposure_us"`
	MinGain         int64  `json:"min_gain" db:"min_gain"`
	MaxGain         int64  `json:"max_gain" db:"max_gain"`
	MinBin          int    `json:"min_bin" db:"min_bin"`
	MaxBin          int    `json:"max_bin" db:"max_bin"`
	MinTempMilliC   int    `json:"min_temp_milli_c" db:"min_temp_milli_c"`
	MaxTempMilliC   int    `json:"max_temp_milli_c" db:"max_temp_milli_c"`
	AvgTempMilliC   int    `json:"avg_temp_milli_c" db:"avg_temp_milli_c"`
	FirstMs         int64  `json:"first_ms" db:"first_ms"`
	LastMs          int64  `json:"last_ms" db:"last_ms"`
}

// CaptureFrameStats aggregates one session's frames per filter and frame type.
//
// The casts are load-bearing: Postgres widens SUM(bigint) and AVG(integer) to numeric, which pgx
// will not scan into an int64/int field.
func (s *Store) CaptureFrameStats(ctx context.Context, sessionID int64) ([]CaptureFrameStat, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT filter, frame_type,
		        COUNT(*)::int                     AS frames,
		        COALESCE(SUM(exposure_us),0)::bigint AS total_exposure_us,
		        MIN(exposure_us)                  AS min_exposure_us,
		        MAX(exposure_us)                  AS max_exposure_us,
		        MIN(gain)                         AS min_gain,
		        MAX(gain)                         AS max_gain,
		        MIN(bin)                          AS min_bin,
		        MAX(bin)                          AS max_bin,
		        MIN(temp_milli_c)                 AS min_temp_milli_c,
		        MAX(temp_milli_c)                 AS max_temp_milli_c,
		        COALESCE(ROUND(AVG(temp_milli_c)),0)::int AS avg_temp_milli_c,
		        MIN(started_at)                   AS first_ms,
		        MAX(started_at)                   AS last_ms
		   FROM capture_frames
		  WHERE session_id=$1
		  GROUP BY filter, frame_type
		  ORDER BY frame_type, LOWER(filter)`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureFrameStat])
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
