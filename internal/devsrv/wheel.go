package devsrv

import (
	"net/http"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Filter-wheel endpoints. Moves are asynchronous on real hardware, so the API mirrors that: the
// position call returns immediately and the caller polls, or asks to wait.

func (s *Server) connectWheel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Driver string   `json:"driver"`
		Names  []string `json:"names"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wheel != nil {
		_ = s.wheel.Close()
		s.wheel = nil
	}
	wheel, err := s.openWheel(body.Driver)
	if err != nil {
		deviceError(w, err)
		return
	}
	if err := wheel.Connect(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	if len(body.Names) > 0 {
		wheel.SetFilterNames(body.Names)
	}
	s.wheel = wheel
	st, _ := wheel.State()
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "wheel": st})
}

func (s *Server) disconnectWheel(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wheel != nil {
		_ = s.wheel.Close()
		s.wheel = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

func (s *Server) wheelStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	wheel := s.wheel
	s.mu.Unlock()
	if wheel == nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	st, err := wheel.State()
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "wheel": st})
}

// setWheelPosition starts a move. With wait=true it blocks until the wheel settles — the sequencer
// uses that, because exposing through a half-open filter silently ruins a sub.
// setWheelNames records what is in each slot on the ALREADY-CONNECTED wheel. A dedicated endpoint
// rather than a reconnect: swapping a filter label mid-session must not drop the USB connection or
// re-home the wheel. POST /wheel/names
func (s *Server) setWheelNames(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	wheel := s.wheel
	s.mu.Unlock()
	if wheel == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	wheel.SetFilterNames(body.Names)
	st, _ := wheel.State()
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "wheel": st})
}

func (s *Server) setWheelPosition(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Slot int  `json:"slot"`
		Wait bool `json:"wait"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	wheel := s.wheel
	s.mu.Unlock()
	if wheel == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := wheel.SetPosition(body.Slot); err != nil {
		deviceError(w, err)
		return
	}
	if body.Wait {
		if err := wheel.WaitSettled(r.Context()); err != nil {
			deviceError(w, err)
			return
		}
	}
	st, _ := wheel.State()
	writeJSON(w, http.StatusOK, map[string]any{"wheel": st})
}

func (s *Server) calibrateWheel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	wheel := s.wheel
	s.mu.Unlock()
	if wheel == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := wheel.Calibrate(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	st, _ := wheel.State()
	writeJSON(w, http.StatusOK, map[string]any{"wheel": st})
}
