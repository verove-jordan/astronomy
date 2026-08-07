package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// The sky a capture session ran under. The analysis and the sampling schedule live in
// internal/skylog; this layer only persists.
//
// These rows exist because the weather the engine can reach is a forecast, not an archive: a night's
// conditions are retrievable for about a day and then gone. Writing them as the session runs is the
// only way the record can exist at all.

// CaptureCondition is one hourly observation of the sky over a running session.
type CaptureCondition struct {
	ID            int64  `json:"id" db:"id"`
	SessionID     int64  `json:"session_id" db:"session_id"`
	AtMs          int64  `json:"at_ms" db:"at_ms"`
	SessionStatus string `json:"session_status" db:"session_status"`

	CloudPct     float64 `json:"cloud_pct" db:"cloud_pct"`
	CloudLow     float64 `json:"cloud_low" db:"cloud_low"`
	CloudMid     float64 `json:"cloud_mid" db:"cloud_mid"`
	CloudHigh    float64 `json:"cloud_high" db:"cloud_high"`
	SeeingArcsec float64 `json:"seeing_arcsec" db:"seeing_arcsec"`
	Transparency float64 `json:"transparency" db:"transparency"`
	HumidityPct  float64 `json:"humidity_pct" db:"humidity_pct"`
	DewPointC    float64 `json:"dew_point_c" db:"dew_point_c"`
	TempC        float64 `json:"temp_c" db:"temp_c"`
	DewSpreadC   float64 `json:"dew_spread_c" db:"dew_spread_c"`
	DewRisk      string  `json:"dew_risk" db:"dew_risk"`
	WindKmh      float64 `json:"wind_kmh" db:"wind_kmh"`
	GustKmh      float64 `json:"gust_kmh" db:"gust_kmh"`
	Jet300Kmh    float64 `json:"jet300_kmh" db:"jet300_kmh"`
	CAPE         float64 `json:"cape" db:"cape"`
	LiftedIndex  float64 `json:"lifted_index" db:"lifted_index"`
	VisibilityM  float64 `json:"visibility_m" db:"visibility_m"`
	PrecipPct    float64 `json:"precip_pct" db:"precip_pct"`
	AOD          float64 `json:"aod" db:"aod"`
	Verdict      float64 `json:"verdict" db:"verdict"`
	KpNow        float64 `json:"kp_now" db:"kp_now"`
	KpMax        float64 `json:"kp_max" db:"kp_max"`
	Aurora       string  `json:"aurora" db:"aurora"`

	MoonIllum         float64 `json:"moon_illum" db:"moon_illum"`
	MoonAltDeg        float64 `json:"moon_alt_deg" db:"moon_alt_deg"`
	MoonAzDeg         float64 `json:"moon_az_deg" db:"moon_az_deg"`
	MoonPhaseAngleDeg float64 `json:"moon_phase_angle_deg" db:"moon_phase_angle_deg"`
	MoonSepDeg        float64 `json:"moon_sep_deg" db:"moon_sep_deg"`
	TargetAltDeg      float64 `json:"target_alt_deg" db:"target_alt_deg"`
	TargetAzDeg       float64 `json:"target_az_deg" db:"target_az_deg"`
	TargetAirmass     float64 `json:"target_airmass" db:"target_airmass"`
	TargetValid       bool    `json:"target_valid" db:"target_valid"`

	SQM    float64 `json:"sqm" db:"sqm"`
	Bortle int     `json:"bortle" db:"bortle"`

	ForecastAgeMs int64  `json:"forecast_age_ms" db:"forecast_age_ms"`
	Source        string `json:"source" db:"source"`

	CreatedAt int64 `json:"created_at" db:"created_at"`
}

const captureConditionCols = `id,session_id,at_ms,session_status,` +
	`cloud_pct,cloud_low,cloud_mid,cloud_high,seeing_arcsec,transparency,humidity_pct,dew_point_c,` +
	`temp_c,dew_spread_c,dew_risk,wind_kmh,gust_kmh,jet300_kmh,cape,lifted_index,visibility_m,` +
	`precip_pct,aod,verdict,kp_now,kp_max,aurora,` +
	`moon_illum,moon_alt_deg,moon_az_deg,moon_phase_angle_deg,moon_sep_deg,` +
	`target_alt_deg,target_az_deg,target_airmass,target_valid,sqm,bortle,` +
	`forecast_age_ms,source,created_at`

// AddCaptureCondition records one observation.
func (s *Store) AddCaptureCondition(ctx context.Context, c CaptureCondition) (int64, error) {
	if c.SessionStatus == "" {
		c.SessionStatus = "running"
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO capture_conditions(session_id,at_ms,session_status,
		   cloud_pct,cloud_low,cloud_mid,cloud_high,seeing_arcsec,transparency,humidity_pct,
		   dew_point_c,temp_c,dew_spread_c,dew_risk,wind_kmh,gust_kmh,jet300_kmh,cape,lifted_index,
		   visibility_m,precip_pct,aod,verdict,kp_now,kp_max,aurora,
		   moon_illum,moon_alt_deg,moon_az_deg,moon_phase_angle_deg,moon_sep_deg,
		   target_alt_deg,target_az_deg,target_airmass,target_valid,sqm,bortle,
		   forecast_age_ms,source,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,
		        $24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40)
		 RETURNING id`,
		c.SessionID, c.AtMs, c.SessionStatus,
		c.CloudPct, c.CloudLow, c.CloudMid, c.CloudHigh, c.SeeingArcsec, c.Transparency, c.HumidityPct,
		c.DewPointC, c.TempC, c.DewSpreadC, c.DewRisk, c.WindKmh, c.GustKmh, c.Jet300Kmh, c.CAPE,
		c.LiftedIndex, c.VisibilityM, c.PrecipPct, c.AOD, c.Verdict, c.KpNow, c.KpMax, c.Aurora,
		c.MoonIllum, c.MoonAltDeg, c.MoonAzDeg, c.MoonPhaseAngleDeg, c.MoonSepDeg,
		c.TargetAltDeg, c.TargetAzDeg, c.TargetAirmass, c.TargetValid, c.SQM, c.Bortle,
		c.ForecastAgeMs, c.Source, nowMs()).Scan(&id)
	return id, err
}

// CaptureConditions returns one session's observations in time order.
func (s *Store) CaptureConditions(ctx context.Context, sessionID int64) ([]CaptureCondition, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureConditionCols+` FROM capture_conditions WHERE session_id=$1 ORDER BY at_ms`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureCondition])
}

// CaptureForecast is the whole hourly forecast as it stood at one end of the session.
type CaptureForecast struct {
	ID        int64  `json:"id" db:"id"`
	SessionID int64  `json:"session_id" db:"session_id"`
	Kind      string `json:"kind" db:"kind"` // "start" | "end"
	AtMs      int64  `json:"at_ms" db:"at_ms"`
	Payload   []byte `json:"payload" db:"payload"` // weather.SiteForecast, passed through verbatim
	CreatedAt int64  `json:"created_at" db:"created_at"`
}

const captureForecastCols = `id,session_id,kind,at_ms,payload,created_at`

// SaveCaptureForecast archives a snapshot, replacing any earlier one of the same kind — a finish
// that runs twice (a resume, a retry) must not leave two competing "end" records.
func (s *Store) SaveCaptureForecast(ctx context.Context, f CaptureForecast) error {
	now := nowMs()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO capture_forecasts(session_id,kind,at_ms,payload,created_at)
		 VALUES($1,$2,$3,$4,$5)
		 ON CONFLICT (session_id, kind) DO UPDATE SET
		   at_ms=EXCLUDED.at_ms, payload=EXCLUDED.payload`,
		f.SessionID, f.Kind, f.AtMs, jsonOrEmpty(f.Payload), now)
	return err
}

// CaptureForecasts returns a session's snapshots, oldest first.
func (s *Store) CaptureForecasts(ctx context.Context, sessionID int64) ([]CaptureForecast, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+captureForecastCols+` FROM capture_forecasts WHERE session_id=$1 ORDER BY at_ms`,
		sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, pgx.RowToStructByName[CaptureForecast])
}
