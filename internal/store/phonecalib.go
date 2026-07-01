package store

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/calib"
)

// SavePhoneMaster inserts (replacing any master with the same identity) a phone calibration master
// into the reusable library. Identity is master_type + iso + exposure + camera_model + dimensions, so
// two darks that differ only by ISO both persist instead of overwriting each other.
func (s *Store) SavePhoneMaster(ctx context.Context, m calib.PhoneMaster) error {
	now := nowMs()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path

	if _, err := tx.Exec(ctx,
		`DELETE FROM phone_calib_masters
		 WHERE master_type=$1 AND iso=$2 AND exposure_ms=$3 AND camera_model=$4
		   AND width=$5 AND height=$6`,
		string(m.Type), m.ISO, m.ExposureMs, m.CameraModel, m.Width, m.Height); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO phone_calib_masters(master_type, iso, exposure_ms, camera_model, width, height,
		    frame_count, path, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)`,
		string(m.Type), m.ISO, m.ExposureMs, m.CameraModel, m.Width, m.Height,
		m.FrameCount, m.Path, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListPhoneMasters returns every phone calibration master in the library.
func (s *Store) ListPhoneMasters(ctx context.Context) ([]calib.PhoneMaster, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT master_type, iso, exposure_ms, camera_model, width, height, frame_count, path
		 FROM phone_calib_masters ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []calib.PhoneMaster
	for rows.Next() {
		var m calib.PhoneMaster
		var mt string
		if err := rows.Scan(&mt, &m.ISO, &m.ExposureMs, &m.CameraModel, &m.Width, &m.Height,
			&m.FrameCount, &m.Path); err != nil {
			return nil, err
		}
		m.Type = calib.MasterType(mt)
		out = append(out, m)
	}
	return out, rows.Err()
}
