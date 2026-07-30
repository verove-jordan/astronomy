package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/tracking"
)

// Tracking analysis: what a night of subs says about the mount.
//
// This is the measurement half of the periodic-error question. It answers, from the user's own
// frames, how much periodic error this mount really has, how fast it drifts, and therefore how long
// an unguided sub can be — figures that are otherwise guesswork or a hand-controller PEC dance.
//
// It does NOT correct anything. Applying a learned curve back to the mount moves hardware on a
// fitted model, and the honest order is to measure first, on several nights, before trusting it.

// trackSink adapts the store to the capture runner's narrow interface.
type trackSink struct{ store *store.Store }

func (t trackSink) AddTrackingSample(ctx context.Context, sessionID int64, tSec, ra, dec float64, source string) error {
	if t.store == nil {
		return nil
	}
	_, err := t.store.AddTrackingSample(ctx, store.TrackingSample{
		SessionID: sessionID, TSec: tSec, RAArcsec: ra, DecArcsec: dec, Source: source,
	})
	return err
}

// trackingReport analyses one capture session's samples. GET /api/tracking/report/{id}
func (s *Server) trackingReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid session id")
		return
	}
	rows, err := s.store.TrackingSamples(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}

	samples := make([]tracking.Sample, 0, len(rows))
	for _, row := range rows {
		samples = append(samples, tracking.Sample{
			TimeSec: row.TSec, RAArcsec: row.RAArcsec, DecArcsec: row.DecArcsec,
		})
	}

	// The worm period and the image scale come from config rather than being guessed: an AVX is not
	// the only mount this could run on, and the scale decides the exposure advice.
	report := tracking.Analyze(samples, s.cfg.MountWormPeriodSec, s.imageScaleArcsecPx())

	out := map[string]any{"session_id": id, "samples": rows, "report": report}
	// If measurement is running but recording nothing, say why. Plate solving fails for ordinary
	// reasons (cloud, a sparse field, no solver configured) and an unexplained empty panel reads as
	// a broken feature.
	if stats, ok := s.captureRunner().TrackStats(); ok {
		out["measuring"] = stats
	}
	if report == nil {
		// Not an error: an early session simply has too few points yet, and saying so is more useful
		// than an empty object the UI has to interpret.
		out["message"] = "not enough measurements yet — a report needs at least a dozen solved frames"
	}
	writeJSON(w, http.StatusOK, out)
}

// trackingSessions lists the sessions that have tracking data, newest first, so the UI can offer a
// history rather than only the run that just finished. GET /api/tracking/sessions
func (s *Server) trackingSessions(w http.ResponseWriter, r *http.Request) {
	ids, err := s.store.RecentTrackingSessions(r.Context(), 20)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_ids": ids})
}

// imageScaleArcsecPx is the plate scale from the configured optics: 206265 × pixel / focal length.
// Zero when either is unset, which the analysis reads as "give no exposure advice".
func (s *Server) imageScaleArcsecPx() float64 {
	if s.cfg.FocalLenMM <= 0 || s.cfg.PixelSizeUm <= 0 {
		return 0
	}
	return 206.265 * s.cfg.PixelSizeUm / s.cfg.FocalLenMM
}
