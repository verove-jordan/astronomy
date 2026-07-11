package store

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// Target is a canonical sky object in the catalog. Frames of the same target (matched by coordinate
// cone or normalized name) can be pooled across sessions to grow integration time.
type Target struct {
	ID             int64    `json:"id"`
	CanonicalName  string   `json:"canonical_name"`
	NormalizedName string   `json:"normalized_name"`
	RADeg          float64  `json:"ra_deg"`
	DecDeg         float64  `json:"dec_deg"`
	HasCoords      bool     `json:"has_coords"`
	Aliases        []string `json:"aliases"`
}

// FrameRow is a persisted frame returned by the reuse queries (a subset of the frames table needed
// to re-link and re-calibrate prior captures).
type FrameRow struct {
	SessionID  int64  `json:"session_id"`
	Path       string `json:"path"`
	FrameType  string `json:"frame_type"`
	Filter     string `json:"filter"`
	ExposureMs int64  `json:"exposure_ms"`
	Gain       int64  `json:"gain"`
	Offset     int64  `json:"cam_offset"`
	Bin        int    `json:"bin"`
	TempMilliC int64  `json:"temp_milli_c"`
	HasTemp    bool   `json:"has_temp"`
	DateObsMs  int64  `json:"date_obs_ms"`
}

// rowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, so target upsert can run inside the
// SaveInventory transaction or standalone.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ResolveTarget upserts a catalog entry for name, filling coordinates when known, and returns it.
func (s *Store) ResolveTarget(ctx context.Context, name string, raDeg, decDeg float64, hasCoords bool) (*Target, error) {
	id, err := upsertTarget(ctx, s.pool, name, raDeg, decDeg, hasCoords)
	if err != nil {
		return nil, err
	}
	return s.targetByID(ctx, id)
}

// upsertTarget inserts a target keyed by its normalized name, or updates coordinates on an existing
// row when this observation supplies them and the catalog lacked them. Returns the target id.
func upsertTarget(ctx context.Context, q rowQuerier, name string, raDeg, decDeg float64, hasCoords bool) (int64, error) {
	norm := skycat.Normalize(name)
	if norm == "" {
		return 0, fmt.Errorf("resolve target: empty name")
	}
	now := nowMs()
	var id int64
	err := q.QueryRow(ctx,
		`INSERT INTO targets(canonical_name, normalized_name, ra_deg, dec_deg, has_coords, created_at, updated_at)
		 VALUES($1,$2,$3,$4,$5,$6,$6)
		 ON CONFLICT (normalized_name) DO UPDATE SET
		    ra_deg     = CASE WHEN targets.has_coords THEN targets.ra_deg ELSE EXCLUDED.ra_deg END,
		    dec_deg    = CASE WHEN targets.has_coords THEN targets.dec_deg ELSE EXCLUDED.dec_deg END,
		    has_coords = targets.has_coords OR EXCLUDED.has_coords,
		    updated_at = EXCLUDED.updated_at
		 RETURNING id`,
		name, norm, raDeg, decDeg, hasCoords, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert target %q: %w", norm, err)
	}
	return id, nil
}

// resolveTargets upserts one catalog target per distinct object name across the frames (using a
// folder-derived fallback for light frames with no OBJECT header), positioning each by the first
// frame that carries coordinates. Returns a map from normalized name to target id for linking frames.
func resolveTargets(ctx context.Context, q rowQuerier, frames []*inspect.Frame, fallbackObject string) (map[string]*int64, error) {
	type rep struct {
		name      string
		ra, dec   float64
		hasCoords bool
	}
	reps := map[string]*rep{}
	for _, fr := range frames {
		name := effectiveObject(fr, fallbackObject)
		norm := skycat.Normalize(name)
		if norm == "" {
			continue
		}
		r := reps[norm]
		if r == nil {
			r = &rep{name: name}
			reps[norm] = r
		}
		if !r.hasCoords {
			if ra, dec, ok := frameCoords(fr); ok {
				r.ra, r.dec, r.hasCoords = ra, dec, true
			}
		}
	}

	ids := make(map[string]*int64, len(reps))
	for norm, r := range reps {
		id, err := upsertTarget(ctx, q, r.name, r.ra, r.dec, r.hasCoords)
		if err != nil {
			return nil, err
		}
		idCopy := id
		ids[norm] = &idCopy
	}
	return ids, nil
}

// effectiveObject is a frame's target name: its OBJECT header, or the folder-derived fallback for a
// light frame that has none (calibration frames stay unnamed — they have no target).
func effectiveObject(fr *inspect.Frame, fallback string) string {
	if fr.Object != "" {
		return fr.Object
	}
	if fr.Type == inspect.Light {
		return fallback
	}
	return ""
}

// folderObject derives a target name from a capture directory's base name (e.g. ".../input/M101" →
// "M101"). Returns "" for a root/empty path.
func folderObject(root string) string {
	base := filepath.Base(root)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return ""
	}
	return base
}

// frameCoords parses a frame's OBJCTRA/OBJCTDEC to decimal degrees. ok is false unless both parse.
func frameCoords(fr *inspect.Frame) (ra, dec float64, ok bool) {
	ra, okRA := skycat.ParseRA(fr.ObjCtRA)
	dec, okDec := skycat.ParseDec(fr.ObjCtDec)
	if !okRA || !okDec {
		return 0, 0, false
	}
	return ra, dec, true
}

func (s *Store) targetByID(ctx context.Context, id int64) (*Target, error) {
	var t Target
	err := s.pool.QueryRow(ctx,
		`SELECT id, canonical_name, normalized_name, ra_deg, dec_deg, has_coords, aliases
		 FROM targets WHERE id=$1`, id).
		Scan(&t.ID, &t.CanonicalName, &t.NormalizedName, &t.RADeg, &t.DecDeg, &t.HasCoords, &t.Aliases)
	if err != nil {
		return nil, fmt.Errorf("get target %d: %w", id, err)
	}
	return &t, nil
}

// LightQuery selects prior light frames of a target. ExcludeSession drops the current session.
type LightQuery struct {
	RADeg          float64
	DecDeg         float64
	HasCoords      bool
	RadiusDeg      float64  // cone radius; <=0 falls back to name-only
	Names          []string // lowercased object names/aliases to match on LOWER(object)
	ExcludeSession int64
}

// PriorLightFrames returns LIGHT frames matching the target by coordinate cone OR normalized name,
// excluding the current session. The cone prefilters on dec (indexed) then applies the great-circle
// distance; the name branch matches LOWER(object) against the supplied aliases.
func (s *Store) PriorLightFrames(ctx context.Context, q LightQuery) ([]FrameRow, error) {
	const sql = `
SELECT session_id, path, frame_type, filter, exposure_ms, gain, cam_offset, bin_x, temp_milli_c, has_temp, date_obs_ms
FROM frames
WHERE frame_type = 'LIGHT' AND session_id <> $1
  AND (
    ( $2 AND has_coords AND dec_deg BETWEEN $3 AND $4
      AND degrees(acos(LEAST(1, GREATEST(-1,
          sin(radians($5))*sin(radians(dec_deg)) +
          cos(radians($5))*cos(radians(dec_deg))*cos(radians(ra_deg - $6)))))) <= $7 )
    OR LOWER(object) = ANY($8)
  )
ORDER BY session_id, filter`
	useCone := q.HasCoords && q.RadiusDeg > 0
	rows, err := s.pool.Query(ctx, sql,
		q.ExcludeSession, useCone, q.DecDeg-q.RadiusDeg, q.DecDeg+q.RadiusDeg,
		q.DecDeg, q.RADeg, q.RadiusDeg, q.Names)
	if err != nil {
		return nil, fmt.Errorf("query prior lights: %w", err)
	}
	defer rows.Close()
	return scanFrameRows(rows)
}

// CalibQuery selects raw calibration frames for a camera signature within a recency window.
type CalibQuery struct {
	Types     []string // e.g. ["DARK","BIAS"]
	Gain      int64
	Offset    int64
	Bin       int
	SinceMs   int64 // 0 = no recency bound
	SessionID int64 // >0 restricts to one session (per-session flats); 0 = any session
}

// RawCalibFrames returns raw calibration frames matching the camera signature (and optionally one
// session) within the recency window. Temperature bucketing is applied by the caller (calib).
func (s *Store) RawCalibFrames(ctx context.Context, q CalibQuery) ([]FrameRow, error) {
	const sql = `
SELECT session_id, path, frame_type, filter, exposure_ms, gain, cam_offset, bin_x, temp_milli_c, has_temp, date_obs_ms
FROM frames
WHERE frame_type = ANY($1) AND gain = $2 AND cam_offset = $3 AND bin_x = $4
  AND ($5 = 0 OR date_obs_ms >= $5)
  AND ($6 = 0 OR session_id = $6)
ORDER BY frame_type, filter, exposure_ms`
	rows, err := s.pool.Query(ctx, sql, q.Types, q.Gain, q.Offset, q.Bin, q.SinceMs, q.SessionID)
	if err != nil {
		return nil, fmt.Errorf("query raw calib: %w", err)
	}
	defer rows.Close()
	return scanFrameRows(rows)
}

// RawCalibPaths implements calib.RawCalibProvider: it returns the raw calibration frames of one type
// for a camera signature within the recency window, so calib can pool them into a deep master.
func (s *Store) RawCalibPaths(ctx context.Context, q calib.RawQuery) ([]calib.RawFrame, error) {
	rows, err := s.RawCalibFrames(ctx, CalibQuery{
		Types: []string{string(q.Type)}, Gain: q.Gain, Offset: q.Offset, Bin: q.Bin, SinceMs: q.SinceMs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]calib.RawFrame, 0, len(rows))
	for _, r := range rows {
		out = append(out, calib.RawFrame{
			Path: r.Path, ExposureMs: r.ExposureMs, TempMilliC: r.TempMilliC,
			HasTemp: r.HasTemp, SessionID: r.SessionID,
		})
	}
	return out, nil
}

// FramesByPaths returns the catalogued frame rows for the given local paths (any frame type), so the
// low-disk remote scan can plan a previously-processed capture straight from the catalog — full
// classification (type/filter/exposure/gain/offset/bin/temp/date) without reading any FITS. Paths not in
// the catalog are simply absent from the result (the caller falls back to a header/token scan for those).
func (s *Store) FramesByPaths(ctx context.Context, paths []string) ([]FrameRow, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	const sql = `
SELECT session_id, path, frame_type, filter, exposure_ms, gain, cam_offset, bin_x, temp_milli_c, has_temp, date_obs_ms
FROM frames
WHERE path = ANY($1)`
	rows, err := s.pool.Query(ctx, sql, paths)
	if err != nil {
		return nil, fmt.Errorf("query frames by paths: %w", err)
	}
	defer rows.Close()
	return scanFrameRows(rows)
}

func scanFrameRows(rows pgx.Rows) ([]FrameRow, error) {
	var out []FrameRow
	for rows.Next() {
		var r FrameRow
		if err := rows.Scan(&r.SessionID, &r.Path, &r.FrameType, &r.Filter, &r.ExposureMs,
			&r.Gain, &r.Offset, &r.Bin, &r.TempMilliC, &r.HasTemp, &r.DateObsMs); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
