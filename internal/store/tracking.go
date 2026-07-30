package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// TrackingSample is one measured pointing error, as stored. The analysis lives in
// internal/tracking; this layer only persists.
type TrackingSample struct {
	ID        int64   `json:"id" db:"id"`
	SessionID int64   `json:"session_id" db:"session_id"`
	TSec      float64 `json:"t_sec" db:"t_sec"`
	RAArcsec  float64 `json:"ra_arcsec" db:"ra_arcsec"`
	DecArcsec float64 `json:"dec_arcsec" db:"dec_arcsec"`
	Source    string  `json:"source" db:"source"`
	CreatedAt int64   `json:"created_at" db:"created_at"`
}

const trackingSampleCols = `id,session_id,t_sec,ra_arcsec,dec_arcsec,source,created_at`

// AddTrackingSample records one frame's measured drift.
func (s *Store) AddTrackingSample(ctx context.Context, sample TrackingSample) (int64, error) {
	source := sample.Source
	if source == "" {
		source = "match"
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tracking_samples(session_id,t_sec,ra_arcsec,dec_arcsec,source,created_at)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		sample.SessionID, sample.TSec, sample.RAArcsec, sample.DecArcsec, source, nowMs()).Scan(&id)
	return id, err
}

// TrackingSamples returns one session's samples in time order.
func (s *Store) TrackingSamples(ctx context.Context, sessionID int64) ([]TrackingSample, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+trackingSampleCols+` FROM tracking_samples WHERE session_id=$1 ORDER BY t_sec`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[TrackingSample])
}

// RecentTrackingSamples gathers the newest sessions' samples together, so a mount's periodic error
// can be characterised across several nights rather than one. Sessions are NOT concatenated in time
// (each t_sec restarts at zero and the worm phase is lost at power-off) — the caller analyses each
// separately and compares.
func (s *Store) RecentTrackingSessions(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.pool.Query(ctx,
		`SELECT session_id FROM tracking_samples
		 GROUP BY session_id ORDER BY MAX(created_at) DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}
