package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/polaralign"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// Polar alignment from the live camera: HTTP.
//
// The route names mirror the procedure rather than the code — start, next, adjust, stop — because that
// is what the panel's buttons say, and a session the user drives by hand is easier to reason about when
// the wire matches the steps.
//
// These sit under /api/capture rather than /api/sky because they need the telescope. The existing
// /api/sky/polar is the open-loop reticle calculation and needs nothing but a clock.

// polarStartBody is the panel's "begin" request. Everything is optional: latitude and longitude fall
// back to the configured site exactly as they do for the reticle.
type polarStartBody struct {
	LatDeg       float64 `json:"lat_deg"`
	LonDeg       float64 `json:"lon_deg"`
	Points       int     `json:"points"`
	ExposureUs   int64   `json:"exposure_us"`
	Gain         int64   `json:"gain"`
	NoRefraction bool    `json:"no_refraction"`
}

// polarSession lazily builds the session, so a server that never polar-aligns pays nothing for it.
func (s *Server) polarSession() *capture.PolarSession {
	s.polarOnce.Do(func() {
		s.polar = capture.NewPolarSession(capture.NewClient(s.cfg.DeviceAddr), s.solver())
	})
	return s.polar
}

// solver is the one place a plate solver is chosen, so the simulated one can never be half-applied —
// measuring with Siril and adjusting with the simulator would produce a beautifully consistent lie.
func (s *Server) solver() capture.Solver {
	if s.cfg.SimSolver {
		return platesolve.NewSimSolver()
	}
	solveOpts, _ := postprocess.SolveSpccFromConfig(s.cfg)
	return platesolve.New(s.sirilRunner, solveOpts)
}

// polarSite resolves where the telescope stands: what the browser sent, else the configured site.
//
// A zeroed pair means "not sent" rather than the Gulf of Guinea, which is the same reading captureSite
// takes of the same ambiguity.
func (s *Server) polarSite(b polarStartBody) polaralign.Site {
	site := polaralign.Site{LatDeg: s.cfg.LatDeg, LonDeg: s.cfg.LonDeg}
	inRange := b.LatDeg >= -90 && b.LatDeg <= 90 && b.LonDeg >= -180 && b.LonDeg <= 180
	if inRange && (b.LatDeg != 0 || b.LonDeg != 0) {
		site.LatDeg, site.LonDeg = b.LatDeg, b.LonDeg
	}
	return site
}

// startPolar begins a measurement and takes the first frame. POST /api/capture/polar/start
func (s *Server) startPolar(w http.ResponseWriter, r *http.Request) {
	var b polarStartBody
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
			badRequest(w, "invalid body")
			return
		}
	}
	sess := s.polarSession()

	// Checked before exposing rather than after four frames: "Siril is not installed" is a setup
	// problem, and finding it out at the end of the procedure wastes the user's night.
	if err := s.polarSolverReady(r); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": err.Error(), "code": "solver_unavailable",
		})
		return
	}
	// One telescope: a sequence already exposing owns the camera.
	if p := s.captureRunner().Progress(); p.Status == capture.StatusRunning {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "a capture session is running — stop it before aligning",
			"code":  "capture_running",
		})
		return
	}

	err := sess.Start(r.Context(), capture.PolarOptions{
		Site:         s.polarSite(b),
		Points:       b.Points,
		ExposureUs:   b.ExposureUs,
		Gain:         b.Gain,
		NoRefraction: b.NoRefraction,
		FocalMM:      s.cfg.FocalLenMM,
		PixelUm:      s.cfg.PixelSizeUm,
		ScratchDir:   s.cfg.WorkDir,
	})
	s.writePolarState(w, sess, err)
}

// nextPolar records that the user has turned the axis and takes the next frame.
// POST /api/capture/polar/next
func (s *Server) nextPolar(w http.ResponseWriter, r *http.Request) {
	sess := s.polarSession()
	s.writePolarState(w, sess, sess.Next(r.Context()))
}

// adjustPolar enters the live phase. POST /api/capture/polar/adjust
func (s *Server) adjustPolar(w http.ResponseWriter, r *http.Request) {
	sess := s.polarSession()
	s.writePolarState(w, sess, sess.Adjust(r.Context()))
}

// refreshPolar takes another frame during the live phase. POST /api/capture/polar/refresh
//
// The browser drives this on a timer rather than the engine looping on its own, so closing the panel
// stops the exposures instead of leaving them running against a page nobody is watching.
func (s *Server) refreshPolar(w http.ResponseWriter, r *http.Request) {
	sess := s.polarSession()
	s.writePolarState(w, sess, sess.Refresh(r.Context()))
}

// stopPolar ends the session. POST /api/capture/polar/stop
func (s *Server) stopPolar(w http.ResponseWriter, _ *http.Request) {
	sess := s.polarSession()
	sess.Stop()
	writeJSON(w, http.StatusOK, map[string]any{"state": sess.Snapshot()})
}

// polarStatus is the snapshot. GET /api/capture/polar
func (s *Server) polarStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"state": s.polarSession().Snapshot()})
}

// polarEvents streams the session over SSE, snapshot first so a reconnecting page is never blank —
// the same shape as the capture stream, so the frontend's EventSource handling applies unchanged.
func (s *Server) polarEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		serverError(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sess := s.polarSession()
	updates, unsubscribe := sess.Subscribe()
	defer unsubscribe()

	send := func(st capture.PolarState) bool {
		payload, err := json.Marshal(st)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send(sess.Snapshot())

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case st, open := <-updates:
			if !open || !send(st) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// polarSolverReady reports whether plate solving can run at all.
func (s *Server) polarSolverReady(r *http.Request) error {
	type available interface{ Available(context.Context) error }
	probe, ok := s.solver().(available)
	if !ok {
		return nil
	}
	if err := probe.Available(r.Context()); err != nil {
		return fmt.Errorf("plate solving needs Siril: %w", err)
	}
	return nil
}

// writePolarState answers with the session state whatever happened.
//
// A failed step still returns 200 with the state, because the state IS the answer: it carries the phase
// the session fell back to and the message to show. Only a request that was wrong to make — a step out
// of order, a busy session — is an error status, since those are the client's to avoid.
func (s *Server) writePolarState(w http.ResponseWriter, sess *capture.PolarSession, err error) {
	switch {
	case errors.Is(err, capture.ErrPolarBusy):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(), "code": "polar_busy", "state": sess.Snapshot(),
		})
	case errors.Is(err, capture.ErrPolarNotRunning):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(), "code": "polar_not_running", "state": sess.Snapshot(),
		})
	case errors.Is(err, capture.ErrPolarNoSolver):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": err.Error(), "code": "solver_unavailable", "state": sess.Snapshot(),
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"state": sess.Snapshot()})
	}
}
