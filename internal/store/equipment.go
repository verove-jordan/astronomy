package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// EquipmentSetup is a named telescope + camera rig. Optics field names match the wire optics the
// planner sends (skyplan.Optics), so a setup can be projected onto a plan request with no renaming.
// Eyepieces is opaque JSON owned by the Tonight eyepiece calculator.
type EquipmentSetup struct {
	ID         int64   `json:"id" db:"id"`
	Name       string  `json:"name" db:"name"`
	FocalMM    float64 `json:"focal_mm" db:"focal_mm"`
	ApertureMM float64 `json:"aperture_mm" db:"aperture_mm"`
	PixelUm    float64 `json:"pixel_um" db:"pixel_um"`
	SensorWpx  int     `json:"sensor_w_px" db:"sensor_w_px"`
	SensorHpx  int     `json:"sensor_h_px" db:"sensor_h_px"`
	BarlowX    float64 `json:"barlow_x" db:"barlow_x"`
	ReducerX   float64 `json:"reducer_x" db:"reducer_x"`
	CameraName string  `json:"camera_name" db:"camera_name"`
	Eyepieces  []byte  `json:"eyepieces" db:"eyepieces"` // JSONB array, passed through verbatim
	Favorite   bool    `json:"favorite" db:"favorite"`
	CreatedAt  int64   `json:"created_at" db:"created_at"`
	UpdatedAt  int64   `json:"updated_at" db:"updated_at"`
}

const equipmentCols = `id,name,focal_mm,aperture_mm,pixel_um,sensor_w_px,sensor_h_px,barlow_x,` +
	`reducer_x,camera_name,eyepieces,favorite,created_at,updated_at`

// ListEquipmentSetups returns every saved rig, favourites first then by lowercased name.
func (s *Store) ListEquipmentSetups(ctx context.Context) ([]EquipmentSetup, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+equipmentCols+` FROM equipment_setups ORDER BY favorite DESC, LOWER(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[EquipmentSetup])
}

// GetEquipmentSetup returns one rig by id.
func (s *Store) GetEquipmentSetup(ctx context.Context, id int64) (EquipmentSetup, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+equipmentCols+` FROM equipment_setups WHERE id=$1`, id)
	if err != nil {
		return EquipmentSetup{}, err
	}
	defer rows.Close()
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[EquipmentSetup])
}

// SaveEquipmentSetup upserts by name (case-insensitively, via the unique LOWER(name) index): saving
// the same rig name after a tweak overwrites it rather than duplicating — the behaviour the
// localStorage version had. Returns the row id.
func (s *Store) SaveEquipmentSetup(ctx context.Context, e EquipmentSetup) (int64, error) {
	if len(e.Eyepieces) == 0 {
		e.Eyepieces = []byte("[]")
	}
	now := nowMs()
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO equipment_setups(name,focal_mm,aperture_mm,pixel_um,sensor_w_px,sensor_h_px,
		   barlow_x,reducer_x,camera_name,eyepieces,favorite,created_at,updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		 ON CONFLICT (LOWER(name)) DO UPDATE SET
		   name=EXCLUDED.name, focal_mm=EXCLUDED.focal_mm, aperture_mm=EXCLUDED.aperture_mm,
		   pixel_um=EXCLUDED.pixel_um, sensor_w_px=EXCLUDED.sensor_w_px,
		   sensor_h_px=EXCLUDED.sensor_h_px, barlow_x=EXCLUDED.barlow_x,
		   reducer_x=EXCLUDED.reducer_x, camera_name=EXCLUDED.camera_name,
		   eyepieces=EXCLUDED.eyepieces, favorite=EXCLUDED.favorite, updated_at=$12
		 RETURNING id`,
		e.Name, e.FocalMM, e.ApertureMM, e.PixelUm, e.SensorWpx, e.SensorHpx,
		e.BarlowX, e.ReducerX, e.CameraName, e.Eyepieces, e.Favorite, now).Scan(&id)
	return id, err
}

// UpdateEquipmentSetup replaces one rig by id (a rename included). The unique index surfaces a name
// collision as an error, which the API maps to 409.
func (s *Store) UpdateEquipmentSetup(ctx context.Context, e EquipmentSetup) error {
	if len(e.Eyepieces) == 0 {
		e.Eyepieces = []byte("[]")
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE equipment_setups SET name=$2, focal_mm=$3, aperture_mm=$4, pixel_um=$5,
		   sensor_w_px=$6, sensor_h_px=$7, barlow_x=$8, reducer_x=$9, camera_name=$10,
		   eyepieces=$11, favorite=$12, updated_at=$13
		 WHERE id=$1`,
		e.ID, e.Name, e.FocalMM, e.ApertureMM, e.PixelUm, e.SensorWpx, e.SensorHpx,
		e.BarlowX, e.ReducerX, e.CameraName, e.Eyepieces, e.Favorite, nowMs())
	return err
}

// DeleteEquipmentSetup forgets a rig.
func (s *Store) DeleteEquipmentSetup(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM equipment_setups WHERE id=$1`, id)
	return err
}
