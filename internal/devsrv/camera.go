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
	}
	if !decodeBody(w, r, &body) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera != nil {
		_ = s.camera.Close()
		s.camera = nil
	}
	cam, err := s.openCamera(body.Driver)
	if err != nil {
		deviceError(w, err)
		return
	}
	if err := cam.Connect(r.Context()); err != nil {
		deviceError(w, err)
		return
	}
	s.camera = cam
	writeJSON(w, http.StatusOK, s.cameraSnapshotLocked())
}

func (s *Server) disconnectCamera(w http.ResponseWriter, _ *http.Request) {
	s.live.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera != nil {
		_ = s.camera.Close()
		s.camera = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": false})
}

func (s *Server) cameraStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.cameraSnapshotLocked())
}

// cameraSnapshotLocked is the full camera state the UI renders from — capabilities, every control
// with its real range, the ROI and the exposure state. Caller holds s.mu.
func (s *Server) cameraSnapshotLocked() map[string]any {
	if s.camera == nil {
		return map[string]any{"connected": false}
	}
	state, _ := s.camera.ExposureState()
	return map[string]any{
		"connected": s.camera.Connected(),
		"caps":      s.camera.Caps(),
		"controls":  s.camera.Controls(),
		"roi":       s.camera.ROI(),
		"exposure":  state,
		"streaming": s.camera.Streaming(),
		"dropped":   s.camera.DroppedFrames(),
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := s.camera.SetControl(body.Name, body.Value, body.Auto); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.cameraSnapshotLocked())
}

func (s *Server) setCameraROI(w http.ResponseWriter, r *http.Request) {
	var roi device.ROI
	if !decodeBody(w, r, &roi) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	applied, err := s.camera.SetROI(roi)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := s.camera.StartExposure(r.Context(), body.Dark); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"exposure": device.ExposureWorking})
}

func (s *Server) abortExposure(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.camera == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	if err := s.camera.AbortExposure(); err != nil {
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
	s.mu.Lock()
	cam := s.camera
	s.mu.Unlock()
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
