package devsrv

import (
	"fmt"
	"net/http"
	"time"

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
	// s.mu is not held across Connect: the NexStar handshake retries over a 9600-baud line and can
	// take seconds, and nothing else in this process may be unanswerable while it does.
	s.mountGate.Lock()
	defer s.mountGate.Unlock()
	s.detachMount()

	mount, err := s.openMount(body.Driver, body.Port)
	if err != nil {
		deviceError(w, err)
		return
	}
	if err := mount.Connect(r.Context()); err != nil {
		_ = mount.Close()
		deviceError(w, err)
		return
	}
	s.attachMount(mount)
	driver := body.Driver
	if driver == "" {
		driver = DriverSim
	}
	// Remembered only once it has actually answered, so a typo in the port never becomes the thing
	// the server tries to reconnect to every time it starts.
	s.link.remember(driver, body.Port, mountModelOf(mount))
	s.link.start()
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "mount": st})
}

// mountModelOf reads the model from whatever driver is connected, without insisting every driver
// expose one.
func mountModelOf(m device.Mount) string {
	if named, ok := m.(interface{ Model() string }); ok {
		return named.Model()
	}
	return ""
}

func (s *Server) disconnectMount(w http.ResponseWriter, _ *http.Request) {
	s.mountGate.Lock()
	defer s.mountGate.Unlock()
	s.detachMount()
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

// mountStatus serves the same snapshot the event stream does, so a polling client and a streaming
// one can never disagree about what the mount is doing.
//
// Note it answers 200 with an "error" field rather than a 5xx when the link is momentarily down: the
// panel needs to show "reconnecting" with the last known position, and a failed request would blank
// it instead.
func (s *Server) mountStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mountSnapshot(r.Context()))
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
		// HoldMs is how long the caller promises to renew within. The browser cannot be trusted with
		// the stop — a tab closed or a laptop slept between pointerdown and pointerup sends no
		// pointerup at all — so the driver stops the axis itself if no renewal arrives in time.
		HoldMs int `json:"hold_ms"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	mount := s.currentMount()
	if mount == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if deadman, ok := mount.(interface{ SetDeadman(time.Duration) }); ok {
		deadman.SetDeadman(time.Duration(body.HoldMs) * time.Millisecond)
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
