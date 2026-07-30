package devsrv

import (
	"errors"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Periodic-error-correction endpoints.
//
// These are the read side plus the two switches: what the mount's worm table holds, where the worm
// is, whether the index has been found and whether playback is running. Computing a curve is not
// here and does not belong here — this file only exposes the hardware.
//
// The one thing worth doing eagerly is `POST /pec/enable`. A Celestron keeps its recorded curve
// across power-off but comes up with playback OFF and the index unfound, so a mount with a perfectly
// good table tracks as if it had none until somebody walks through the hand controller's menus. That
// is a real nightly loss with a one-call fix.

// errNoPEC is returned when the connected mount has no PEC table. It is a capability gap, not a
// failure: the UI hides the panel rather than showing an error.
var errNoPEC = errors.New("this mount has no periodic-error correction table")

// pecMount returns the connected mount as a PECMount, or nil when it has no table.
func (s *Server) pecMount() device.PECMount {
	mount := s.currentMount()
	if mount == nil {
		return nil
	}
	pec, ok := mount.(device.PECMount)
	if !ok {
		return nil
	}
	return pec
}

// pecTarget resolves the mount and writes the right refusal if there is not one. It returns nil when
// the caller should stop.
func (s *Server) pecTarget(w http.ResponseWriter) device.PECMount {
	if s.currentMount() == nil {
		deviceError(w, device.ErrNotConnected)
		return nil
	}
	pec := s.pecMount()
	if pec == nil {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false, "reason": errNoPEC.Error()})
		return nil
	}
	return pec
}

func (s *Server) pecStatus(w http.ResponseWriter, r *http.Request) {
	if s.currentMount() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "supported": false})
		return
	}
	pec := s.pecMount()
	if pec == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": true, "supported": false, "reason": errNoPEC.Error(),
		})
		return
	}
	caps, err := pec.PECCaps(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	st, err := pec.PECStatus(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connected": true, "supported": true, "caps": caps, "status": st,
	})
}

// pecCurve reads the table the mount is holding. This is also how a curve is backed up before
// anything overwrites it — the only copy of a hand-recorded curve lives in the mount.
func (s *Server) pecCurve(w http.ResponseWriter, r *http.Request) {
	pec := s.pecTarget(w)
	if pec == nil {
		return
	}
	curve, err := pec.PECReadCurve(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	// int8 marshals as a number; send it as a plain array so the UI can chart it directly.
	bins := make([]int, len(curve))
	for i, v := range curve {
		bins[i] = int(v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "bins": bins})
}

func (s *Server) pecSeekIndex(w http.ResponseWriter, r *http.Request) {
	pec := s.pecTarget(w)
	if pec == nil {
		return
	}
	// The seek MOVES the mount, by up to two degrees in RA, so it must not run while the camera is
	// mid-exposure on a framed target.
	if s.live.isRunning() {
		s.live.stop()
	}
	if err := pec.PECSeekIndex(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	st, err := pec.PECStatus(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "status": st})
}

func (s *Server) pecPlayback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	pec := s.pecTarget(w)
	if pec == nil {
		return
	}
	if err := pec.PECPlayback(r.Context(), body.On); err != nil {
		deviceError(w, err)
		return
	}
	st, err := pec.PECStatus(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"supported": true, "status": st})
}

// pecEnable is the whole "use the curve the mount already has" flow in one call: find the index if it
// is not found, then start playback.
//
// It is separate from pecSeekIndex + pecPlayback because the ordering is not optional — playback
// before the index is found replays the curve against an unknown worm position, which is worse than
// not replaying it at all — and because this is the call a capture session makes at startup, where
// two round trips and a state machine in the caller would be two chances to get it wrong.
func (s *Server) pecEnable(w http.ResponseWriter, r *http.Request) {
	pec := s.pecTarget(w)
	if pec == nil {
		return
	}
	st, err := pec.PECStatus(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	sought := false
	if !st.Indexed {
		if s.live.isRunning() {
			s.live.stop()
		}
		if err := pec.PECSeekIndex(r.Context()); err != nil {
			deviceError(w, err)
			return
		}
		sought = true
	}
	if err := pec.PECPlayback(r.Context(), true); err != nil {
		deviceError(w, err)
		return
	}
	st, err = pec.PECStatus(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"supported": true, "status": st,
		// The caller needs to know whether framing survived: a seek turns RA by up to two degrees.
		"sought_index": sought,
	})
}
