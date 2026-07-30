package devsrv

import (
	"fmt"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

// Mount endpoints. Everything that can physically move the telescope is guarded here rather than in
// the UI: a mount can drive its own tube into the tripod, so the refusals must live on the side
// that actually issues the command.

// minAltitudeDeg is the floor below which a GoTo is refused. Pointing into the ground is never
// intentional, and on a GEM it is how a tube meets a tripod leg.
const minAltitudeDeg = 5

func (s *Server) connectMount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Driver string `json:"driver"`
		Port   string `json:"port"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mount != nil {
		_ = s.mount.Close()
		s.mount = nil
	}
	// Remember the chosen port before the driver is built: it is what the factory opens.
	if body.Port != "" {
		mountPort = body.Port
	}
	mount, err := s.openMount(body.Driver)
	if err != nil {
		deviceError(w, err)
		return
	}
	if porter, ok := mount.(interface{ SetPort(string) }); ok && body.Port != "" {
		porter.SetPort(body.Port)
	}
	if err := mount.Connect(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	s.mount = mount
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "mount": st})
}

func (s *Server) disconnectMount(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mount != nil {
		_ = s.mount.Close()
		s.mount = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

func (s *Server) mountStatus(w http.ResponseWriter, r *http.Request) {
	mount := s.currentMount()
	if mount == nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	// A periodic-error run reads the worm's position several times a second, and every mount command
	// queues behind the same single-command-in-flight mutex on a 9600-baud link. A UI polling for
	// status would steal that time from the measurement, so while a run owns the port it is served
	// the state the run itself refreshes.
	if st, ok := s.pecTrain.cachedMountState(); ok {
		writeJSON(w, http.StatusOK, map[string]any{"connected": true, "mount": st, "cached": true})
		return
	}
	st, err := mount.State(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "mount": st})
}

func (s *Server) currentMount() device.Mount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mount
}

// mountGoto slews to J2000 coordinates, refusing anything below the altitude floor. force skips the
// altitude check for the rare deliberate case (a daytime alignment star, an indoor test).
func (s *Server) mountGoto(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RADeg  float64 `json:"ra_deg"`
		DecDeg float64 `json:"dec_deg"`
		Force  bool    `json:"force"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if !body.Force {
		if alt, ok := s.altitudeOf(body.RADeg, body.DecDeg); ok && alt < minAltitudeDeg {
			badRequest(w, fmt.Sprintf(
				"target is %.1f° above the horizon (below the %d° safety floor) — send force to override",
				alt, minAltitudeDeg))
			return
		}
	}
	if err := mount.GotoRADec(r.Context(), body.RADeg, body.DecDeg); err != nil {
		deviceError(w, err)
		return
	}
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusAccepted, map[string]any{"mount": st})
}

// altitudeOf is the target's current altitude at the configured site; ok is false when the site is
// unknown, in which case the floor cannot be enforced and the caller proceeds.
func (s *Server) altitudeOf(raDeg, decDeg float64) (float64, bool) {
	if s.cfg == nil || (s.cfg.LatDeg == 0 && s.cfg.LonDeg == 0) {
		return 0, false
	}
	alt, _ := astro.Horizontal(raDeg, decDeg, s.cfg.LatDeg, s.cfg.LonDeg, nowFunc())
	return alt, true
}

func (s *Server) mountSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RADeg  float64 `json:"ra_deg"`
		DecDeg float64 `json:"dec_deg"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := mount.Sync(r.Context(), body.RADeg, body.DecDeg); err != nil {
		deviceError(w, err)
		return
	}
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"mount": st})
}

// mountAbort is the STOP button. It must work even when other calls are failing, so it takes no
// body and never validates anything.
func (s *Server) mountAbort(w http.ResponseWriter, r *http.Request) {
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := mount.Abort(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) mountJog(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Direction string `json:"direction"`
		Rate      int    `json:"rate"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := mount.Jog(r.Context(), device.Direction(body.Direction), body.Rate); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// mountNudge moves by an exact angular amount — the dither primitive.
func (s *Server) mountNudge(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RAArcsec  float64 `json:"ra_arcsec"`
		DecArcsec float64 `json:"dec_arcsec"`
		// Measure watches a star across the move and reports how far it ACTUALLY went. A commanded
		// eight-pixel dither on a German equatorial can achieve three, because the declination gear
		// spends the first part of any reversal taking up backlash — so a dither planner that
		// believes its own commands slowly loses the careful spread it was chosen for.
		Measure       bool    `json:"measure"`
		MeasureExpSec float64 `json:"measure_exposure_sec"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if body.Measure {
		s.nudgeMeasured(w, r, mount, body.RAArcsec, body.DecArcsec, body.MeasureExpSec)
		return
	}
	if err := mount.Nudge(r.Context(), body.RAArcsec, body.DecArcsec); err != nil {
		deviceError(w, err)
		return
	}
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"mount": st})
}

func (s *Server) mountTracking(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On   bool   `json:"on"`
		Rate string `json:"rate"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := mount.SetTracking(r.Context(), body.On, body.Rate); err != nil {
		deviceError(w, err)
		return
	}
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"mount": st})
}
