// Package devsrv is the device server: a small HTTP service that owns the camera, filter wheel and
// mount and exposes them over localhost.
//
// It runs as its OWN process (`astrostack device`) rather than inside the engine, for four reasons
// that all bite in practice: `just dev` restarts the engine on every source save and would drop the
// USB connection mid-sequence; a crash inside a vendor SDK takes down its process, and losing the
// device server is survivable where losing a 3-hour stack is not; the engine's worker lanes
// saturate the CPU during stacking while live view and focus metering must keep their cadence; and
// keeping it separate leaves the option of building just this binary for a different architecture.
// It mirrors cmd/graxpert-host, the same host-sidecar pattern already used for GraXpert.
//
// The split of responsibilities is deliberate: this process moves hardware and hands back pixels,
// while the engine owns sequencing, the database, file naming and mosaic knowledge. Frames are
// written to disk at a path the engine chooses, so image bytes never cross HTTP except for the
// live preview stream.
package devsrv

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/sim"
)

// Server holds the currently connected devices. Exactly one of each kind can be connected at a
// time — a second camera on the same mount is not a scenario worth the complexity.
type Server struct {
	cfg *config.Config

	mu     sync.Mutex
	camera device.Camera
	wheel  device.FilterWheel
	mount  device.Mount
	world  *sim.World // non-nil when the simulated driver is in use

	live     *liveView
	video    *videoRecorder
	pecTrain *pecSession
	// link supervises the mount's serial link: an idle-timer heartbeat, the health the UI streams,
	// and the one thing this process must remember across a restart — which port worked.
	link *mountLink
}

// New builds a device server. It connects nothing: the UI (or the sequencer) picks devices
// explicitly, so plugging in hardware never surprises a running session.
func New(cfg *config.Config) *Server {
	s := &Server{cfg: cfg}
	s.live = newLiveView(s)
	s.video = newVideoRecorder(s)
	s.pecTrain = newPECSession(s)
	s.link = newMountLink(s)
	// Reconnect to the hand controller that worked last time, in the background. A mount that is not
	// plugged in tonight must leave the server starting normally.
	s.link.restore()
	return s
}

// Handler wires the routes. Paths are relative to the service root; the engine reverse-proxies
// /api/device/* onto them.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /devices", s.listDevices)
	mux.HandleFunc("GET /ports", s.listSerialPorts)

	mux.HandleFunc("POST /camera/connect", s.connectCamera)
	mux.HandleFunc("POST /camera/disconnect", s.disconnectCamera)
	mux.HandleFunc("GET /camera", s.cameraStatus)
	mux.HandleFunc("POST /camera/control", s.setCameraControl)
	mux.HandleFunc("POST /camera/roi", s.setCameraROI)
	mux.HandleFunc("POST /camera/expose", s.startExposure)
	mux.HandleFunc("POST /camera/abort", s.abortExposure)
	mux.HandleFunc("POST /camera/save", s.saveExposure)

	mux.HandleFunc("POST /wheel/connect", s.connectWheel)
	mux.HandleFunc("POST /wheel/disconnect", s.disconnectWheel)
	mux.HandleFunc("GET /wheel", s.wheelStatus)
	mux.HandleFunc("POST /wheel/position", s.setWheelPosition)
	mux.HandleFunc("POST /wheel/calibrate", s.calibrateWheel)

	mux.HandleFunc("POST /mount/connect", s.connectMount)
	mux.HandleFunc("POST /mount/disconnect", s.disconnectMount)
	mux.HandleFunc("GET /mount", s.mountStatus)
	mux.HandleFunc("GET /mount/events", s.mountEvents)
	mux.HandleFunc("GET /mount/link", s.mountLinkStatus)
	mux.HandleFunc("GET /mount/axes", s.mountAxes)
	mux.HandleFunc("POST /mount/site", s.mountSite)
	mux.HandleFunc("POST /mount/clock", s.mountClock)
	mux.HandleFunc("GET /diagnose", s.mountDiagnose)
	mux.HandleFunc("POST /mount/goto", s.mountGoto)
	mux.HandleFunc("POST /mount/sync", s.mountSync)
	mux.HandleFunc("POST /mount/abort", s.mountAbort)
	mux.HandleFunc("POST /mount/jog", s.mountJog)
	mux.HandleFunc("POST /mount/nudge", s.mountNudge)
	mux.HandleFunc("GET /mount/guide", s.guideStatus)
	mux.HandleFunc("POST /mount/guide", s.guidePulse)
	mux.HandleFunc("POST /mount/guide-rate", s.guideSetRate)
	mux.HandleFunc("POST /mount/tracking", s.mountTracking)
	mux.HandleFunc("GET /mount/audit", s.mountAudit)
	mux.HandleFunc("POST /mount/reset", s.mountReset)

	mux.HandleFunc("GET /pec", s.pecStatus)
	mux.HandleFunc("GET /pec/curve", s.pecCurve)
	mux.HandleFunc("POST /pec/index/seek", s.pecSeekIndex)
	mux.HandleFunc("POST /pec/playback", s.pecPlayback)
	mux.HandleFunc("POST /pec/enable", s.pecEnable)
	mux.HandleFunc("POST /pec/train/start", s.pecTrainStart)
	mux.HandleFunc("POST /pec/train/stop", s.pecTrainStop)
	mux.HandleFunc("GET /pec/train", s.pecTrainStatus)
	mux.HandleFunc("GET /pec/train/events", s.pecTrainEvents)

	mux.HandleFunc("POST /live/start", s.liveStart)
	mux.HandleFunc("POST /live/stop", s.liveStop)
	mux.HandleFunc("GET /live/frame", s.liveFrame)
	mux.HandleFunc("GET /live/stats", s.liveStats)
	mux.HandleFunc("GET /live/events", s.liveEvents)
	mux.HandleFunc("POST /live/simulate", s.liveSimulate)
	mux.HandleFunc("POST /live/save", s.liveSave)
	mux.HandleFunc("POST /live/focus/reset", s.liveFocusReset)
	mux.HandleFunc("POST /video/start", s.videoStart)
	mux.HandleFunc("POST /video/stop", s.videoStop)
	mux.HandleFunc("GET /video/status", s.videoStatus)
	mux.HandleFunc("POST /flat-exposure", s.flatExposure)
	mux.HandleFunc("POST /wheel/names", s.setWheelNames)
	mux.HandleFunc("POST /prepare-dir", s.prepareDir)

	return cors(mux)
}

// Close disconnects everything — called on shutdown so a cooler is not left running.
func (s *Server) Close() {
	s.live.stop()
	s.link.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera != nil {
		_ = s.camera.Close()
	}
	if s.wheel != nil {
		_ = s.wheel.Close()
	}
	if s.mount != nil {
		_ = s.mount.Close()
	}
}

// Drivers reports the availability of every device driver in this build.
func (s *Server) Drivers() []DriverStatus { return driverReport() }

// health reports which drivers this build can actually use. The engine surfaces it exactly like
// Siril's or GraXpert's availability, so a missing SDK reads as "not installed" rather than as a
// broken feature.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"drivers": driverReport(),
		"connected": map[string]bool{
			"camera": s.camera != nil && s.camera.Connected(),
			"wheel":  s.wheel != nil && s.wheel.Connected(),
			"mount":  s.mount != nil && s.mount.Connected(),
		},
	})
}

// listDevices enumerates what could be connected right now, per driver.
func (s *Server) listDevices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"devices": discover()})
}

// listSerialPorts offers the serial devices a mount might be on. The hand controller shows up as a
// USB-serial adapter; the list is filtered but not guessed at, because a second adapter would be
// ambiguous and silently opening the wrong one is worse than asking.
func (s *Server) listSerialPorts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ports": SerialPorts()})
}

// simWorld lazily creates the shared simulated observatory, so the simulated camera, wheel and
// mount all see the same sky.
func (s *Server) simWorld() *sim.World {
	if s.world == nil {
		s.world = sim.NewWorld(sim.Config{
			FocalMM:    s.cfg.FocalLenMM,
			PixelUm:    s.cfg.PixelSizeUm,
			SensorW:    s.cfg.SensorWpx,
			SensorH:    s.cfg.SensorHpx,
			ApertureMM: s.cfg.ApertureMM,
			LatDeg:     s.cfg.LatDeg,
			LonDeg:     s.cfg.LonDeg,
		})
	}
	return s.world
}

// --- small HTTP helpers (this service is deliberately dependency-free of the engine's api pkg) ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func badRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

// deviceError maps a driver error onto a status code: "not connected" and "busy" are normal states
// the UI shows, not server failures.
func deviceError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errorsIs(err, device.ErrNotConnected):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "not_connected"})
	case errorsIs(err, device.ErrBusy):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "busy"})
	case errorsIs(err, device.ErrUnsupported):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "unsupported"})
	case errorsIs(err, device.ErrDriverUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error(), "code": "driver_unavailable"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		badRequest(w, fmt.Sprintf("invalid body: %v", err))
		return false
	}
	return true
}

// cors allows the browser to reach the device server directly during development (the engine
// proxies it in normal use).
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
