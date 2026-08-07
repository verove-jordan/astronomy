package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/skylog"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// The logbook's conditions half: what the sky was doing while a session ran.
//
// The engine can only see a FORECAST — internal/weather asks Open-Meteo for the last day and the next
// two, and there is no archive endpoint anywhere — so conditions older than about a day cannot be
// recovered. They are therefore sampled live, for the length of the session, and this file is the
// wiring: two adapters onto internal/skylog plus the read endpoint the logbook detail page uses.
//
// Weather and light pollution are reached ONLY through the server's existing nil-safe shims
// (weatherAt, siteAt). Nothing here imports the providers' internals, so the logbook adds no coupling
// to packages that are evolving independently.

// skylogSource adapts the server's providers to the sampler's read side.
type skylogSource struct{ s *Server }

func (a skylogSource) Forecast(ctx context.Context, lat, lon float64) (weather.SiteForecast, string) {
	return a.s.weatherAt(ctx, lat, lon)
}

func (a skylogSource) Site(ctx context.Context, lat, lon float64) (lightpollution.SiteQuality, string) {
	return a.s.siteAt(ctx, lat, lon)
}

// skylogSink adapts the store to the sampler's write side, the way trackSink does for tracking.
type skylogSink struct{ store *store.Store }

func (k skylogSink) AddSample(ctx context.Context, sessionID int64, s skylog.Sample) error {
	if k.store == nil {
		return nil
	}
	_, err := k.store.AddCaptureCondition(ctx, store.CaptureCondition{
		SessionID: sessionID, AtMs: s.AtMs, SessionStatus: s.SessionStatus,
		CloudPct: s.CloudPct, CloudLow: s.CloudLow, CloudMid: s.CloudMid, CloudHigh: s.CloudHigh,
		SeeingArcsec: s.SeeingArcsec, Transparency: s.Transparency, HumidityPct: s.HumidityPct,
		DewPointC: s.DewPointC, TempC: s.TempC, DewSpreadC: s.DewSpreadC, DewRisk: s.DewRisk,
		WindKmh: s.WindKmh, GustKmh: s.GustKmh, Jet300Kmh: s.Jet300Kmh, CAPE: s.CAPE,
		LiftedIndex: s.LiftedIndex, VisibilityM: s.VisibilityM, PrecipPct: s.PrecipPct, AOD: s.AOD,
		Verdict: s.Verdict, KpNow: s.KpNow, KpMax: s.KpMax, Aurora: s.Aurora,
		MoonIllum: s.MoonIllum, MoonAltDeg: s.MoonAltDeg, MoonAzDeg: s.MoonAzDeg,
		MoonPhaseAngleDeg: s.MoonPhaseAngleDeg, MoonSepDeg: s.MoonSepDeg,
		TargetAltDeg: s.TargetAltDeg, TargetAzDeg: s.TargetAzDeg, TargetAirmass: s.TargetAirmass,
		TargetValid: s.TargetValid, SQM: s.SQM, Bortle: s.Bortle,
		ForecastAgeMs: s.ForecastAgeMs, Source: s.Source,
	})
	return err
}

func (k skylogSink) SaveSummary(ctx context.Context, sessionID int64, sum skylog.Summary) error {
	if k.store == nil {
		return nil
	}
	payload, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	return k.store.SetCaptureSessionConditions(ctx, sessionID, payload)
}

func (k skylogSink) SaveForecast(ctx context.Context, sessionID int64, kind string, atMs int64, f weather.SiteForecast) error {
	if k.store == nil {
		return nil
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return k.store.SaveCaptureForecast(ctx, store.CaptureForecast{
		SessionID: sessionID, Kind: kind, AtMs: atMs, Payload: payload,
	})
}

// conditionsInterval is how often a running session samples the sky.
func (s *Server) conditionsInterval() time.Duration {
	if s.cfg == nil || s.cfg.ConditionsIntervalMin <= 0 {
		return skylog.DefaultInterval
	}
	return time.Duration(s.cfg.ConditionsIntervalMin) * time.Minute
}

// attachConditionsLogger starts recording the sky for the session that just began, and stops when
// the sequencer reports a terminal status.
//
// It mirrors attachTrackMonitor: entirely optional, and a missing weather provider simply means no
// record — never a refused capture. The run outlives the HTTP request that started it, so the logger
// gets a background context rather than the request's.
func (s *Server) attachConditionsLogger(sessionID int64, site skylog.Site, tgt skylog.Target) {
	if sessionID == 0 || s.weather == nil || s.store == nil {
		s.conditionsLog.Store(nil)
		return
	}
	lg := skylog.New(skylogSource{s: s}, skylogSink{store: s.store}, s.conditionsInterval())
	if lg == nil {
		s.conditionsLog.Store(nil)
		return
	}
	s.conditionsLog.Store(lg)

	// Subscribing to the progress stream is what makes this work without touching the sequencer: the
	// runner already fans out every state change, and a terminal status is the session's end.
	progress, unsubscribe := s.captureRunner().Subscribe()
	done := make(chan struct{})
	go func() {
		defer unsubscribe()
		for p := range progress {
			if p.SessionID == sessionID && isTerminalCaptureStatus(p.Status) {
				close(done)
				return
			}
		}
		// The channel closed without a terminal status (the runner went away); end the recording
		// rather than leaving it sampling a session nobody is driving.
		close(done)
	}()

	go lg.Run(context.Background(), sessionID, site, tgt, done, func() string {
		return string(s.captureRunner().Progress().Status)
	})
}

// isTerminalCaptureStatus reports whether a session has stopped for good. "interrupted" is written
// only by the boot-time orphan sweep, never by the runner, so it cannot appear on this stream — but
// it is listed here so the set stays honest if that ever changes.
func isTerminalCaptureStatus(st capture.Status) bool {
	switch st {
	case capture.StatusCompleted, capture.StatusAborted, capture.StatusFailed:
		return true
	}
	return false
}

// captureConditions returns the full conditions record for one session: every sample, the archived
// forecast snapshots, and the rolled-up summary. GET /api/capture/sessions/{id}/conditions
//
// This is deliberately a separate endpoint from the session itself — the forecast blobs are tens of
// kilobytes each and only the logbook's detail view wants them.
func (s *Server) captureConditions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid session id")
		return
	}
	sess, err := s.store.GetCaptureSession(r.Context(), id)
	if err != nil {
		badRequest(w, "unknown session")
		return
	}
	rows, err := s.store.CaptureConditions(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	forecasts, err := s.store.CaptureForecasts(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}

	out := map[string]any{
		"session_id": id,
		"conditions": rows,
		"forecasts":  captureForecastsJSON(forecasts),
		"summary":    json.RawMessage(rawOrEmpty(sess.ConditionsSummary, "{}")),
		"site":       map[string]any{"lat": sess.SiteLat, "lon": sess.SiteLon, "elevation_m": sess.SiteElevationM},
	}
	// An empty record reads as a broken feature unless it says why. Recording only starts with the
	// session, so every run from before this shipped has nothing — and cannot be backfilled, because
	// the weather providers have no archive.
	if len(rows) == 0 {
		out["message"] = "no conditions were recorded for this session — the sky is sampled while a " +
			"session runs and cannot be reconstructed afterwards"
	}
	if stats, ok := s.conditionsLog.Load().Stats(); ok {
		out["recording"] = stats
	}
	writeJSON(w, http.StatusOK, out)
}

// captureForecastsJSON projects the archived snapshots, turning the JSONB payload into raw JSON —
// without this encoding/json would base64 the []byte.
func captureForecastsJSON(rows []store.CaptureForecast) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		out = append(out, map[string]any{
			"kind": f.Kind, "at_ms": f.AtMs,
			"payload": json.RawMessage(rawOrEmpty(f.Payload, "{}")),
		})
	}
	return out
}
