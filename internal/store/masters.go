package store

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/calib"
)

// SaveMaster inserts (replacing any master with the same identity) a master calibration frame
// into the reusable library.
func (s *Store) SaveMaster(ctx context.Context, m calib.Master) error {
	now := nowMs()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on the error path

	if _, err := tx.Exec(ctx,
		`DELETE FROM master_frames
		 WHERE master_type=$1 AND filter=$2 AND exposure_ms=$3 AND gain=$4
		   AND cam_offset=$5 AND bin=$6 AND temp_milli_c=$7`,
		string(m.Type), m.Filter, m.ExposureMs, m.Gain, m.Offset, m.Bin, m.TempMilliC); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO master_frames(master_type, filter, exposure_ms, gain, cam_offset,
		    temp_milli_c, bin, frame_count, path, instrument, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		string(m.Type), m.Filter, m.ExposureMs, m.Gain, m.Offset, m.TempMilliC, m.Bin,
		m.FrameCount, m.Path, "", now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListMasters returns every master in the library.
func (s *Store) ListMasters(ctx context.Context) ([]calib.Master, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT master_type, filter, exposure_ms, gain, cam_offset, temp_milli_c, bin, frame_count, path
		 FROM master_frames ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []calib.Master
	for rows.Next() {
		var m calib.Master
		var mt string
		if err := rows.Scan(&mt, &m.Filter, &m.ExposureMs, &m.Gain, &m.Offset,
			&m.TempMilliC, &m.Bin, &m.FrameCount, &m.Path); err != nil {
			return nil, err
		}
		m.Type = calib.MasterType(mt)
		m.HasTemp = m.TempMilliC != 0
		out = append(out, m)
	}
	return out, rows.Err()
}
