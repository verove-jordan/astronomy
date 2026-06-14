package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/migrations"
)

type migration struct {
	version int64
	name    string
}

// Migrate applies every embedded *.up.sql migration not yet recorded, each in its own
// transaction, tracking applied versions in schema_migrations.
func (s *Store) Migrate(ctx context.Context) (applied int, err error) {
	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY, applied_at BIGINT NOT NULL)`); err != nil {
		return 0, fmt.Errorf("ensure schema_migrations: %w", err)
	}
	ups, err := listMigrations(".up.sql")
	if err != nil {
		return 0, err
	}
	for _, m := range ups {
		done, err := s.isApplied(ctx, m.version)
		if err != nil {
			return applied, err
		}
		if done {
			continue
		}
		body, err := migrations.FS.ReadFile(m.name)
		if err != nil {
			return applied, err
		}
		if err := s.applyTx(ctx, m, string(body)); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// MigrateDown rolls back the most recently applied migration using its *.down.sql.
func (s *Store) MigrateDown(ctx context.Context) error {
	var version int64
	err := s.pool.QueryRow(ctx,
		`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version)
	if err != nil {
		return fmt.Errorf("no applied migration to roll back: %w", err)
	}
	downs, err := listMigrations(".down.sql")
	if err != nil {
		return err
	}
	for _, m := range downs {
		if m.version != version {
			continue
		}
		body, err := migrations.FS.ReadFile(m.name)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, version); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	return fmt.Errorf("no down migration for version %d", version)
}

func (s *Store) applyTx(ctx context.Context, m migration, body string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path
	if _, err := tx.Exec(ctx, body); err != nil {
		return fmt.Errorf("apply %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES($1,$2)`, m.version, nowMs()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) isApplied(ctx context.Context, version int64) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, version).Scan(&n)
	return n > 0, err
}

func listMigrations(suffix string) ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, suffix) {
			continue
		}
		idx := strings.IndexByte(name, '_')
		if idx < 0 {
			continue
		}
		v, err := strconv.ParseInt(name[:idx], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, migration{version: v, name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
