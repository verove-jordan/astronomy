package store

import (
	"context"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SavedSelection is a named (and optionally starred) folder-set from the Import "Processing
// history". It is matched onto the derived history rows by signature, so the same folder-set
// carries its name/star across every run — and survives job pruning as an orphan entry.
type SavedSelection struct {
	ID        int64  `json:"id" db:"id"`
	Name      string `json:"name" db:"name"`
	Favorite  bool   `json:"favorite" db:"favorite"`
	Signature string `json:"signature" db:"signature"`
	Paths     []byte `json:"paths" db:"paths"` // JSONB array of original-case folder paths
	Mode      string `json:"mode" db:"mode"`
	Format    string `json:"format" db:"format"`
	CreatedAt int64  `json:"created_at" db:"created_at"`
	UpdatedAt int64  `json:"updated_at" db:"updated_at"`
}

// SelectionSignature is THE folder-set key: lowercase every path, sort, join with "|". It is the
// single producer for both /api/processed groups and saved_selections rows, so the frontend only
// ever compares two backend-made strings (no Go/JS normalization drift).
func SelectionSignature(paths []string) string {
	lowered := make([]string, len(paths))
	for i, p := range paths {
		lowered[i] = strings.ToLower(p)
	}
	sort.Strings(lowered)
	return strings.Join(lowered, "|")
}

const selectionCols = `id,name,favorite,signature,paths,mode,format,created_at,updated_at`

// ListSelections returns every saved selection, ordered by lowercased name.
func (s *Store) ListSelections(ctx context.Context) ([]SavedSelection, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+selectionCols+` FROM saved_selections ORDER BY LOWER(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[SavedSelection])
}

// SaveSelection upserts by signature: naming the same folder-set again renames it in place rather
// than duplicating. favorite nil leaves an existing star untouched (like SavePreset); non-nil sets
// it (starring an unnamed history row names it with favorite=true in one call). Returns its id.
func (s *Store) SaveSelection(ctx context.Context, name string, pathsJSON []byte, signature, mode, format string, favorite *bool) (int64, error) {
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO saved_selections(name,favorite,signature,paths,mode,format,created_at,updated_at)
		 VALUES($1,COALESCE($2,FALSE),$3,$4,$5,$6,$7,$7)
		 ON CONFLICT (signature) DO UPDATE SET
		   name=EXCLUDED.name, paths=EXCLUDED.paths, mode=EXCLUDED.mode, format=EXCLUDED.format,
		   favorite=COALESCE($2, saved_selections.favorite), updated_at=$7
		 RETURNING id`,
		name, favorite, signature, pathsJSON, mode, format, now).Scan(&id)
	return id, err
}

// RenameSelection changes a saved selection's name. The unique index on LOWER(name) surfaces a
// collision as an error (the API maps it to 409).
func (s *Store) RenameSelection(ctx context.Context, id int64, name string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE saved_selections SET name=$2, updated_at=$3 WHERE id=$1`,
		id, name, nowMs())
	return err
}

// SetSelectionFavorite stars/unstars a saved selection (favorites pin to the top of the history).
func (s *Store) SetSelectionFavorite(ctx context.Context, id int64, favorite bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE saved_selections SET favorite=$2, updated_at=$3 WHERE id=$1`,
		id, favorite, nowMs())
	return err
}

// DeleteSelection forgets a saved selection (its history row reverts to a plain derived entry).
func (s *Store) DeleteSelection(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM saved_selections WHERE id=$1`, id)
	return err
}
