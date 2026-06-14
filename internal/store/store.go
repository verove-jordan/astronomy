// Package store is the Postgres persistence layer (pgx/v5). It owns the connection pool,
// migrations, and typed access to sessions, frames, metrics, masters, jobs and outputs.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool and verifies connectivity.
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres at %s: %w", dsn, err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the connection pool.
func (s *Store) Close() { s.pool.Close() }

// Pool exposes the underlying pool for packages that need direct queries.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func nowMs() int64 { return time.Now().UnixMilli() }

// SaveInventory persists a scanned inventory as a new session with its frames, returning the
// session id. Session and frames are written in one transaction.
func (s *Store) SaveInventory(ctx context.Context, inv *inspect.Inventory) (int64, error) {
	now := nowMs()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path

	var sessionID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO sessions(root_path, object, created_at, updated_at)
		 VALUES($1,$2,$3,$3) RETURNING id`,
		inv.Root, dominantObject(inv), now).Scan(&sessionID); err != nil {
		return 0, fmt.Errorf("create session: %w", err)
	}

	frames := make([]*inspect.Frame, 0, len(inv.Frames)+len(inv.Videos))
	frames = append(frames, inv.Frames...)
	frames = append(frames, inv.Videos...)
	for _, fr := range frames {
		if _, err := tx.Exec(ctx,
			`INSERT INTO frames(session_id, path, frame_type, filter, exposure_ms, gain,
			    cam_offset, temp_milli_c, has_temp, bin_x, bin_y, width, height, object,
			    instrument, date_obs_ms, class_source, created_at, updated_at)
			 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18)`,
			sessionID, fr.Path, string(fr.Type), fr.Filter, fr.ExposureMs, fr.Gain,
			fr.Offset, fr.TempMilliC, fr.HasTemp, fr.BinX, fr.BinY, fr.Width, fr.Height,
			fr.Object, fr.Instrument, fr.DateObsMs, fr.ClassSource, now); err != nil {
			return 0, fmt.Errorf("insert frame %s: %w", fr.Path, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return sessionID, nil
}

// Session is a stored capture session.
type Session struct {
	ID        int64  `json:"id" db:"id"`
	RootPath  string `json:"root_path" db:"root_path"`
	Object    string `json:"object" db:"object"`
	Note      string `json:"note" db:"note"`
	CreatedAt int64  `json:"created_at" db:"created_at"`
	UpdatedAt int64  `json:"updated_at" db:"updated_at"`
}

// ListSessions returns sessions most-recent first.
func (s *Store) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, root_path, object, note, created_at, updated_at
		 FROM sessions ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Session])
}

// CountFramesByType returns the number of frames of each type in a session.
func (s *Store) CountFramesByType(ctx context.Context, sessionID int64) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT frame_type, COUNT(*) FROM frames WHERE session_id=$1 GROUP BY frame_type`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, rows.Err()
}

func dominantObject(inv *inspect.Inventory) string {
	counts := make(map[string]int)
	for _, f := range inv.Frames {
		if f.Type == inspect.Light && f.Object != "" {
			counts[f.Object]++
		}
	}
	best, bestN := "", 0
	for obj, n := range counts {
		if n > bestN {
			best, bestN = obj, n
		}
	}
	return best
}
