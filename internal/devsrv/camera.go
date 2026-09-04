package devsrv

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// Camera endpoints. The engine drives these one step at a time (set filter → set controls → expose
// → save) rather than handing over a whole sequence, so all the state that matters for a session —
// what was captured, where it went, how far the plan has got — stays in the database.

func (s *Server) connectCamera(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Driver string `json:"driver"`
		// Device is the id of the discovered camera to open, as its driver understands it: an
		// AVFoundation device name for a phone. Empty lets the driver choose.
		Device string `json:"device"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	// The gate serialises camera connects; s.mu is taken only to swap the pointer. Holding s.mu
	// across Connect is what used to freeze the whole server, because an iPhone's warm-up is twenty
	// seconds and /health sat behind the same lock.
	s.camGate.Lock()
	defer s.camGate.Unlock()

	// The live loop holds the camera it started with, so a swap must stop it first — otherwise it
	// spends the rest of the session exposing a closed device. Anything recording that loop goes
	// with it: the frames after a camera swap are a different camera's.
	s.liveRec.Stop()
	s.live.stop()
	s.detachCamera()

	cam, err := s.openCamera(body.Driver, body.Device)
	if err != nil {
		deviceError(w, err)
		return
	}
	if err := cam.Connect(r.Context()); err != nil {
		_ = cam.Close()
		deviceError(w, err)
		return
	}
	s.attachCamera(cam)
	writeJSON(w, http.StatusOK, cameraSnapshot(cam))
}

func (s *Server) disconnectCamera(w http.ResponseWriter, _ *http.Request) {
	s.camGate.Lock()
	defer s.camGate.Unlock()
	s.liveRec.Stop()
	s.live.stop()
	s.detachCamera()
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

func (s *Server) cameraStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, cameraSnapshot(s.currentCamera()))
}

// cameraSnapshot is the full camera state the UI renders from — capabilities, every control with its
// real range, the ROI and the exposure state.
func cameraSnapshot(cam device.Camera) map[string]any {
	if cam == nil {
		return map[string]any{"connected": false}
	}
	state, _ := cam.ExposureState()
	return map[string]any{
		"connected": cam.Connected(),
		"caps":      cam.Caps(),
		"controls":  cam.Controls(),
		"roi":       cam.ROI(),
		"exposure":  state,
		"streaming": cam.Streaming(),
		"dropped":   cam.DroppedFrames(),
	}
}

func (s *Server) setCameraControl(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Value int64  `json:"value"`
		Auto  bool   `json:"auto"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := cam.SetControl(body.Name, body.Value, body.Auto); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cameraSnapshot(cam))
}

func (s *Server) setCameraROI(w http.ResponseWriter, r *http.Request) {
	var roi device.ROI
	if !decodeBody(w, r, &roi) {
		return
	}
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	applied, err := cam.SetROI(roi)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roi": applied})
}

func (s *Server) startExposure(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dark bool `json:"dark"`
	}
	_ = decodeBody(w, r, &body) // an empty body means "a normal light frame"
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := cam.StartExposure(r.Context(), body.Dark); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"exposure": device.ExposureWorking})
}

func (s *Server) abortExposure(w http.ResponseWriter, _ *http.Request) {
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := cam.AbortExposure(); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exposure": device.ExposureIdle})
}

// saveRequest is the engine's "download that exposure and write it here" instruction. The metadata
// comes from the engine because only it knows the target, the mosaic tile and the session; the
// device server fills in what only it can measure (actual exposure, gain, sensor temperature).
type saveRequest struct {
	Path string `json:"path"` // absolute file path chosen by the engine

	Type      string  `json:"type"`
	Filter    string  `json:"filter"`
	Object    string  `json:"object"`
	Telescope string  `json:"telescope"`
	FocalMM   float64 `json:"focal_mm"`
	RADeg     float64 `json:"ra_deg"`
	DecDeg    float64 `json:"dec_deg"`
	HasCoord  bool    `json:"has_coord"`
	Panel     string  `json:"panel"`
	SessionID string  `json:"session_id"`
	TargetTem float64 `json:"target_temp_c"`
	HasTarget bool    `json:"has_target_temp"`
}

// saveExposure downloads the finished frame and writes it as a 16-bit FITS with the full capture
// header. Nothing else in the system writes capture files, so the header contract lives in exactly
// one place (internal/device.FrameMeta).
func (s *Server) saveExposure(w http.ResponseWriter, r *http.Request) {
	var body saveRequest
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		badRequest(w, "path is required")
		return
	}
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	frame, err := cam.Download(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	if out, err := writeFrame(frame, cam.Caps(), body); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	} else {
		writeJSON(w, http.StatusOK, out)
	}
}

// writeFrame writes one downloaded frame as a 16-bit FITS with the full capture header, and describes
// what it wrote. Nothing else in the system writes capture files, so the header contract lives in
// exactly one place (internal/device.FrameMeta) — and the live-view saver shares this function rather
// than growing a second, quietly diverging copy of it.
func writeFrame(frame *device.Frame, caps device.CameraCaps, body saveRequest) (map[string]any, error) {
	meta := device.FrameMeta{
		Type: body.Type, Filter: body.Filter,
		ExposureUs: frame.ExposureUs, Gain: frame.Gain, Offset: frame.Offset,
		Bin: frame.Bin, TempMilliC: frame.TempMilliC, HasTemp: frame.HasTemp,
		TargetTempC: body.TargetTem, HasTargetTemp: body.HasTarget,
		Object: body.Object, Instrume: caps.Name, Telescop: body.Telescope,
		FocalLenMM: body.FocalMM, PixelSizeUm: caps.PixelSizeUm,
		RADeg: body.RADeg, DecDeg: body.DecDeg, HasCoord: body.HasCoord,
		Panel: body.Panel, SessionID: body.SessionID,
		StartedAt: frame.StartedAt,
	}
	if err := os.MkdirAll(filepath.Dir(body.Path), 0o755); err != nil {
		return nil, err
	}
	cards := append(meta.Cards(), frame.ExtraCards...)
	if err := fits.Write16(body.Path, frame.Width, frame.Height, frame.Pix, cards); err != nil {
		return nil, err
	}
	return map[string]any{
		"path":         body.Path,
		"width":        frame.Width,
		"height":       frame.Height,
		"exposure_us":  frame.ExposureUs,
		"gain":         frame.Gain,
		"temp_milli_c": frame.TempMilliC,
		"started_at":   frame.StartedAt.Format(time.RFC3339Nano),
	}, nil
}
