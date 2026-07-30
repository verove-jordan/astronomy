package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/capture"
	"github.com/verove-jordan/astronomy/internal/localfs"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/store"
)

// The capture API: start/pause/resume/abort an auto-run, watch it, and look back at what was shot.
// The sequencer itself lives in the engine (internal/capture) rather than in the device server,
// because a session is a statement about a target and a night, and that belongs with the database.

// captureStartBody is the launch payload. The root is validated against the data directory, the way
// every other path-taking endpoint here is: a sequence must not be able to write outside it.
type captureStartBody struct {
	Sequence     capture.Sequence `json:"sequence"`
	Path         string           `json:"path"`
	Object       string           `json:"object"`
	Panel        string           `json:"panel"`
	Telescope    string           `json:"telescope"`
	FocalMM      float64          `json:"focal_mm"`
	MosaicPlanID int64            `json:"mosaic_plan_id"`
	TileIndex    *int             `json:"tile_index"`

	DitherRadiusPx     float64 `json:"dither_radius_px"`
	ImageScaleArcsecPx float64 `json:"image_scale_arcsec_px"`

	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	// MeasureTracking solves a share of the lights to characterise the mount. Opt-in: it costs CPU
	// that the user may want for a livestack running alongside.
	MeasureTracking bool `json:"measure_tracking"`
}

// prepareCaptureDir has the DEVICE SERVER create the destination and verify it is writable.
//
// The device server owns this because it owns the writing: it runs natively on the host, so an
// external drive is a normal writable folder to it, while a containerized engine sees the same drive
// read-only. Doing it here, up front, means "read-only file system" is reported when you press Start
// rather than discovered after the first frame.
func (s *Server) prepareCaptureDir(ctx context.Context, root string) error {
	body, err := json.Marshal(map[string]string{"path": root})
	if err != nil {
		return err
	}
	url := "http://" + s.cfg.DeviceAddr + "/prepare-dir"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s", deviceUnavailableMessage(err, s.cfg.DeviceAddr))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var out struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if out.Error != "" {
		return errors.New(out.Error)
	}
	return fmt.Errorf("the device server could not prepare %s (%s)", root, resp.Status)
}

// attachTrackMonitor turns mount measurement on or off for the session about to start. A missing
// Siril simply means no measurement — never a refused capture.
func (s *Server) attachTrackMonitor(ctx context.Context, enabled bool) {
	if !enabled {
		s.captureRunner().SetTrackMonitor(nil)
		return
	}
	solveOpts, _ := postprocess.SolveSpccFromConfig(s.cfg)
	solver := platesolve.New(s.sirilRunner, solveOpts)
	if err := solver.Available(ctx); err != nil {
		s.captureRunner().SetTrackMonitor(nil)
		return
	}
	s.captureRunner().SetTrackMonitor(
		capture.NewTrackMonitor(solver, trackSink{store: s.store}, s.cfg.TrackingSolveEveryNth))
}

// captureRoot resolves and validates where a session may write.
//
// Unlike every other path-taking endpoint this is NOT confined to the data directory: a night's raw
// frames routinely go straight to an external disk, and forcing them through the app folder first
// would mean copying tens of gigabytes by hand afterwards. The allow-list is the same one the local
// browser already uses (removable-media roots + the app's own input/output/work dirs), so the reach
// is identical to what the UI can browse — and it is re-validated here, never trusted from the client.
//
// The target folder usually does not exist yet ("…/M31/2026-07-27/p03"), so validation walks up to
// the nearest EXISTING ancestor, checks that against the allow-list, and then creates the rest.
func (s *Server) captureRoot(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("a capture folder is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid capture folder")
	}
	roots := s.localAllowRoots()

	// Walk up to something that exists — a new night's subfolder is normal.
	anchor := abs
	for {
		if _, err := os.Stat(anchor); err == nil {
			break
		}
		parent := filepath.Dir(anchor)
		if parent == anchor {
			return "", fmt.Errorf("capture folder is outside the allowed locations")
		}
		anchor = parent
	}
	if _, ok := localfs.Allowed(roots, anchor); !ok {
		return "", fmt.Errorf(
			"capture folder must be inside the data directory or a connected drive")
	}
	// Creation is deliberately NOT done here. The device server writes the frames, and it runs on the
	// host; this engine may be in a container where the drive is mounted read-only. See prepareDir.
	return abs, nil
}

// captureRunner is the engine's single sequencer — one telescope, one session.
func (s *Server) captureRunner() *capture.Runner { return s.capture }

// startCapture launches an auto-run. POST /api/capture/start
func (s *Server) startCapture(w http.ResponseWriter, r *http.Request) {
	var b captureStartBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	root, err := s.captureRoot(b.Path)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	req := capture.Request{
		Sequence: b.Sequence, Root: root, Object: b.Object, Panel: b.Panel,
		Telescope: b.Telescope, FocalMM: b.FocalMM,
		MosaicPlanID: b.MosaicPlanID, TileIndex: b.TileIndex,
		DitherRadiusPx: b.DitherRadiusPx, ImageScaleArcsecPx: b.ImageScaleArcsecPx,
		RADeg: b.RADeg, DecDeg: b.DecDeg,
		PixelUm: s.cfg.PixelSizeUm,
	}
	if req.FocalMM == 0 {
		req.FocalMM = s.cfg.FocalLenMM
	}
	// Ask the writer to create the folder and prove it is writable, before a single exposure is taken.
	if err := s.prepareCaptureDir(r.Context(), root); err != nil {
		badRequest(w, err.Error())
		return
	}
	s.attachTrackMonitor(r.Context(), b.MeasureTracking)
	progress, err := s.captureRunner().Start(r.Context(), req)
	if err != nil {
		if errors.Is(err, capture.ErrSessionRunning) {
			writeJSON(w, http.StatusConflict,
				map[string]any{"error": err.Error(), "code": "session_running", "progress": progress})
			return
		}
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"progress": progress})
}

// centerCapture runs the plate-solve centring loop: expose, solve, sync, re-slew until the target
// sits where it should. POST /api/capture/center
func (s *Server) centerCapture(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RADeg           float64 `json:"ra_deg"`
		DecDeg          float64 `json:"dec_deg"`
		ExposureUs      int64   `json:"exposure_us"`
		Gain            int64   `json:"gain"`
		ToleranceArcsec float64 `json:"tolerance_arcsec"`
		MaxIterations   int     `json:"max_iterations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	solveOpts, _ := postprocess.SolveSpccFromConfig(s.cfg)
	solver := platesolve.New(s.sirilRunner, solveOpts)
	if err := solver.Available(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "plate solving needs Siril: " + err.Error(),
			"code":  "solver_unavailable",
		})
		return
	}
	res, err := s.captureRunner().Center(r.Context(), solver, b.RADeg, b.DecDeg, capture.CenterOptions{
		ExposureUs: b.ExposureUs, Gain: b.Gain,
		ToleranceArcsec: b.ToleranceArcsec, MaxIterations: b.MaxIterations,
		FocalMM: s.cfg.FocalLenMM, PixelUm: s.cfg.PixelSizeUm,
		ScratchDir: s.cfg.WorkDir,
	})
	if err != nil {
		// A partial result is still worth returning: it shows how far the loop got before stopping.
		writeJSON(w, http.StatusOK, map[string]any{"result": res, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) pauseCapture(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"progress": s.captureRunner().Pause()})
}

func (s *Server) resumeCapture(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"progress": s.captureRunner().Resume()})
}

func (s *Server) abortCapture(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"progress": s.captureRunner().Abort()})
}

func (s *Server) captureStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"progress": s.captureRunner().Progress()})
}

// captureEvents streams session progress over SSE — the same shape as the job stream, so the
// frontend's existing EventSource handling applies unchanged.
func (s *Server) captureEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		serverError(w, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	updates, unsubscribe := s.captureRunner().Subscribe()
	defer unsubscribe()

	send := func(p capture.Progress) bool {
		payload, err := json.Marshal(p)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	send(s.captureRunner().Progress()) // snapshot first, so a reconnecting page is never blank

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case p, open := <-updates:
			if !open || !send(p) {
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

// listCaptureSessions returns the recent sessions. GET /api/capture/sessions
func (s *Server) listCaptureSessions(w http.ResponseWriter, r *http.Request) {
	limit := clampAtoi(r.URL.Query().Get("limit"), 50, 1, 500)
	rows, err := s.store.ListCaptureSessions(r.Context(), limit)
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, captureSessionJSON(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// getCaptureSession returns one session with its frames. GET /api/capture/sessions/{id}
func (s *Server) getCaptureSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	row, err := s.store.GetCaptureSession(r.Context(), id)
	if err != nil {
		badRequest(w, "unknown session")
		return
	}
	frames, err := s.store.ListCaptureFrames(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": captureSessionJSON(row),
		"frames":  frames,
	})
}

func captureSessionJSON(row store.CaptureSession) map[string]any {
	return map[string]any{
		"id": row.ID, "object": row.Object, "root": row.Root, "panel": row.Panel,
		"mosaic_plan_id": row.MosaicPlanID, "tile_index": row.TileIndex,
		"sequence":     json.RawMessage(rawOrEmpty(row.Sequence, "{}")),
		"status":       row.Status,
		"progress":     json.RawMessage(rawOrEmpty(row.Progress, "{}")),
		"total_frames": row.TotalFrames, "frames_done": row.FramesDone,
		"started_at": row.StartedAt, "ended_at": row.EndedAt,
	}
}

// --- saved sequences ------------------------------------------------------------------------

func (s *Server) listCaptureSequences(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListCaptureSequences(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id": row.ID, "name": row.Name, "favorite": row.Favorite,
			"payload":    json.RawMessage(rawOrEmpty(row.Payload, "{}")),
			"created_at": row.CreatedAt, "updated_at": row.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sequences": out})
}

func (s *Server) saveCaptureSequence(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name     string           `json:"name"`
		Sequence capture.Sequence `json:"sequence"`
		Favorite bool             `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	if b.Name == "" {
		badRequest(w, "name is required")
		return
	}
	// Validate before saving: a stored sequence that cannot run is a trap for a future night.
	if err := b.Sequence.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}
	payload, err := json.Marshal(b.Sequence)
	if err != nil {
		serverError(w, err)
		return
	}
	id, err := s.store.SaveCaptureSequence(r.Context(), b.Name, payload, b.Favorite)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) deleteCaptureSequence(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.store.DeleteCaptureSequence(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// captureRecorder adapts the store to the sequencer's Recorder interface, keeping internal/capture
// free of the database.
type captureRecorder struct{ store *store.Store }

func (c captureRecorder) CreateSession(ctx context.Context, req capture.Request, total int) (int64, error) {
	seq, _ := json.Marshal(req.Sequence)
	tile := -1
	if req.TileIndex != nil {
		tile = *req.TileIndex
	}
	return c.store.CreateCaptureSession(ctx, store.CaptureSession{
		Object: req.Object, Root: req.Root, Panel: req.Panel,
		MosaicPlanID: req.MosaicPlanID, TileIndex: tile,
		Sequence: seq, Status: string(capture.StatusRunning), TotalFrames: total,
	})
}

func (c captureRecorder) UpdateSession(ctx context.Context, id int64, status capture.Status, p capture.Progress) error {
	payload, _ := json.Marshal(p)
	terminal := status == capture.StatusCompleted || status == capture.StatusAborted ||
		status == capture.StatusFailed
	return c.store.UpdateCaptureSession(ctx, id, string(status), payload, p.FrameIndex, terminal)
}

func (c captureRecorder) RecordFrame(ctx context.Context, sessionID int64, f capture.FrameRecord) error {
	return c.store.RecordCaptureFrame(ctx, store.CaptureFrame{
		SessionID: sessionID, Path: f.Path, Filter: f.Filter, FrameType: f.Type,
		ExposureUs: f.ExposureUs, Gain: f.Gain, FrameOffset: f.Offset, Bin: f.Bin,
		TempMilliC: f.TempMilliC, Panel: f.Panel, SequenceNo: f.Sequence,
		StartedAt: f.StartedAt.UnixMilli(),
	})
}
