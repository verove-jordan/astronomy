package devsrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The training run's HTTP surface. Progress is streamed rather than polled: a run lasts most of an
// hour and the interesting parts — the star being chosen, the drive stopping, the verdict — arrive
// unpredictably.

func (s *Server) pecTrainStart(w http.ResponseWriter, r *http.Request) {
	var req PECTrainRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// Still exposures and video streaming are mutually exclusive on the camera, so a run cannot start
	// on top of a recording.
	if s.video != nil && s.video.Status().Running {
		badRequest(w, "a video recording is using the camera")
		return
	}
	if err := s.pecTrain.start(req); err != nil {
		switch {
		case errors.Is(err, errPECBusy):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		case errors.Is(err, errNoPEC):
			writeJSON(w, http.StatusOK, map[string]any{"supported": false, "reason": err.Error()})
		default:
			deviceError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "state": s.pecTrain.snapshot()})
}

// pecTrainStop asks the run to wind up. It does not abandon what was measured: the samples so far
// are still worth reporting on, and the mount is always left tracking.
func (s *Server) pecTrainStop(w http.ResponseWriter, _ *http.Request) {
	s.pecTrain.stop()
	writeJSON(w, http.StatusOK, map[string]any{"stopped": true, "state": s.pecTrain.snapshot()})
}

func (s *Server) pecTrainStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"running": s.pecTrain.isRunning(),
		"state":   s.pecTrain.snapshot(),
	})
}

func (s *Server) pecTrainEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	updates, unsubscribe := s.pecTrain.subscribe()
	defer unsubscribe()

	send := func() bool {
		payload, err := json.Marshal(map[string]any{
			"running": s.pecTrain.isRunning(),
			"state":   s.pecTrain.snapshot(),
		})
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send() // immediate snapshot, so a reconnecting page is not blank

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			if !send() {
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
