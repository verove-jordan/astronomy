package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// settingLibraryMirror is the app_settings key holding the calibration-library S3 mirror location.
const settingLibraryMirror = "library_mirror"

// LibraryMirrorLocation is where the calibration library was last copied to on S3 (bucket + user prefix),
// recorded so any run can pull a matched master back from <prefix>/library/ when it is absent locally.
type LibraryMirrorLocation struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

// Setting reads the value for key, or ("", false, nil) when the key is absent.
func (s *Store) Setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting upserts key=value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO app_settings(key,value,updated_at) VALUES($1,$2,$3)
		 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`,
		key, value, nowMs())
	return err
}

// SetLibraryMirror records where the calibration library was copied to on S3 (called when the user runs the
// "Copy library to S3" sync), so later runs know which bucket/prefix to pull a missing master from.
func (s *Store) SetLibraryMirror(ctx context.Context, loc LibraryMirrorLocation) error {
	b, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, settingLibraryMirror, string(b))
}

// LibraryMirror returns the recorded library S3 mirror location, or (zero, false) when the library has
// never been copied to S3 (so the puller stays a no-op).
func (s *Store) LibraryMirror(ctx context.Context) (LibraryMirrorLocation, bool, error) {
	v, ok, err := s.Setting(ctx, settingLibraryMirror)
	if err != nil || !ok {
		return LibraryMirrorLocation{}, false, err
	}
	var loc LibraryMirrorLocation
	if err := json.Unmarshal([]byte(v), &loc); err != nil {
		return LibraryMirrorLocation{}, false, err
	}
	return loc, loc.Bucket != "", nil
}
