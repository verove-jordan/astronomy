package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Preset is a user-saved processing preset: a name + a JSON payload (a partial /api/jobs body — the
// situation recipe the UI re-applies to the launch form). Built-in presets are NOT stored here; they
// live in internal/preset and are merged in by the API layer.
type Preset struct {
	ID        int64  `json:"id" db:"id"`
	Name      string `json:"name" db:"name"`
	Payload   []byte `json:"payload" db:"payload"` // JSONB; opaque to the store
	CreatedAt int64  `json:"created_at" db:"created_at"`
	UpdatedAt int64  `json:"updated_at" db:"updated_at"`
}

const presetCols = `id,name,payload,created_at,updated_at`

// ListPresets returns all saved presets, ordered by lowercased name for a stable, human-friendly UI order.
func (s *Store) ListPresets(ctx context.Context) ([]Preset, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+presetCols+` FROM processing_presets ORDER BY LOWER(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[Preset])
}

// SavePreset upserts a preset by case-insensitive name: re-saving under an existing name overwrites its
// payload (so tweaking then re-saving does not duplicate), otherwise inserts a new row. Returns its id.
func (s *Store) SavePreset(ctx context.Context, name string, payload []byte) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO processing_presets(name,payload,created_at,updated_at)
		 VALUES($1,$2,$3,$3)
		 ON CONFLICT (LOWER(name)) DO UPDATE SET payload=EXCLUDED.payload, updated_at=$3
		 RETURNING id`,
		name, payload, now).Scan(&id)
	return id, err
}

// RenamePreset changes a preset's name. The unique index on LOWER(name) surfaces a collision as an error
// (the API maps it to 409); the caller has already validated the name is non-empty.
func (s *Store) RenamePreset(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE processing_presets SET name=$2, updated_at=$3 WHERE id=$1`,
		id, name, nowMs())
	return err
}

// DeletePreset removes a saved preset.
func (s *Store) DeletePreset(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM processing_presets WHERE id=$1`, id)
	return err
}
