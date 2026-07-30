package devsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/ser"
)

// Video recording, for planetary work.
//
// Deep sky wants a few long exposures; planets want thousands of very short ones, so that the rare
// moments of steady seeing can be picked out and stacked. That is a different capture mode: the
// camera streams continuously and every frame goes straight to disk in one SER file.
//
// The recorder writes as fast as the camera delivers and NEVER buffers unboundedly — a planetary
// run at 100 fps on a large sensor outruns any disk eventually, and a recorder that queued would
// end the session by exhausting memory. Dropped frames are counted and reported instead, which is
// the honest failure: a planetary stack of 4900 frames is no worse than one of 5000.

// videoRecorder owns one recording.
type videoRecorder struct {
	srv *Server

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	writer   *ser.Writer
	path     string
	frames   int
	dropped  int
	started  time.Time
	lastErr  string
	finished bool
}

// VideoStatus is what the UI shows while recording.
type VideoStatus struct {
	Running    bool    `json:"running"`
	Path       string  `json:"path,omitempty"`
	Frames     int     `json:"frames"`
	Dropped    int     `json:"dropped"`
	ElapsedSec float64 `json:"elapsed_sec"`
	FPS        float64 `json:"fps"`
	LastError  string  `json:"last_error,omitempty"`
	Finished   bool    `json:"finished"`
}

// VideoOptions start a recording.
type VideoOptions struct {
	Path       string `json:"path"`
	ExposureUs int64  `json:"exposure_us"`
	Gain       int64  `json:"gain"`
	// MaxFrames and MaxSeconds stop the recording on their own. Planetary captures are bounded by
	// how long the target stays steady, and an unbounded recording fills a disk unattended.
	MaxFrames  int     `json:"max_frames"`
	MaxSeconds float64 `json:"max_seconds"`
	Object     string  `json:"object"`
	Telescope  string  `json:"telescope"`
}

func newVideoRecorder(s *Server) *videoRecorder { return &videoRecorder{srv: s} }

// Start opens the file and begins streaming. It returns once recording is under way.
func (v *videoRecorder) Start(opts VideoOptions) error {
	if opts.Path == "" {
		return fmt.Errorf("a recording path is required")
	}
	if filepath.Ext(opts.Path) == "" {
		opts.Path += ".ser"
	}

	v.mu.Lock()
	if v.running {
		v.mu.Unlock()
		return fmt.Errorf("a recording is already running")
	}
	v.mu.Unlock()

	cam := v.srv.currentCamera()
	if cam == nil {
		return device.ErrNotConnected
	}
	if opts.ExposureUs > 0 {
		if err := cam.SetControl(device.ControlExposure, opts.ExposureUs, false); err != nil {
			return err
		}
	}
	if opts.Gain > 0 {
		if err := cam.SetControl(device.ControlGain, opts.Gain, false); err != nil {
			return err
		}
	}

	roi := cam.ROI()
	caps := cam.Caps()
	// The SER header is fixed for the whole file, so it is written from the ROI as it is NOW. That
	// is also why the ROI must not change mid-recording — every later frame would be misaligned.
	w, err := ser.Create(opts.Path, ser.Options{
		Width: roi.Width, Height: roi.Height, BitDepth: 16,
		ColorID:   colorIDFor(caps),
		Observer:  opts.Object,
		Camera:    caps.Name,
		Telescope: opts.Telescope,
	})
	if err != nil {
		return err
	}
	if err := cam.StartVideo(context.Background()); err != nil {
		_ = w.Close()
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	v.mu.Lock()
	v.running, v.finished = true, false
	v.cancel, v.writer, v.path = cancel, w, opts.Path
	v.frames, v.dropped, v.lastErr = 0, 0, ""
	v.started = time.Now()
	v.mu.Unlock()

	go v.loop(ctx, cam, opts)
	return nil
}

// loop pulls frames until told to stop or a limit is reached.
func (v *videoRecorder) loop(ctx context.Context, cam device.Camera, opts VideoOptions) {
	defer v.finish(cam)
	for {
		if ctx.Err() != nil {
			return
		}
		frame, err := cam.NextFrame(ctx, 5*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A timeout mid-stream is a dropped frame, not a failed recording: USB hiccups happen and
			// losing one frame out of thousands costs nothing.
			v.mu.Lock()
			v.dropped++
			v.lastErr = err.Error()
			v.mu.Unlock()
			continue
		}

		v.mu.Lock()
		writer, running := v.writer, v.running
		v.mu.Unlock()
		if !running || writer == nil {
			return
		}
		if err := writer.WriteFrame16(frame.Pix, frame.StartedAt); err != nil {
			// A write failure IS fatal — the file is now inconsistent, and continuing would append
			// frames after a gap that no reader could detect.
			v.mu.Lock()
			v.lastErr = err.Error()
			v.mu.Unlock()
			return
		}
		v.mu.Lock()
		v.frames++
		frames := v.frames
		elapsed := time.Since(v.started).Seconds()
		v.mu.Unlock()

		if opts.MaxFrames > 0 && frames >= opts.MaxFrames {
			return
		}
		if opts.MaxSeconds > 0 && elapsed >= opts.MaxSeconds {
			return
		}
	}
}

// finish closes the file and stops the camera's stream. Closing is what writes the true frame count
// into the header, so a recording that skipped it would read as empty.
func (v *videoRecorder) finish(cam device.Camera) {
	v.mu.Lock()
	w := v.writer
	v.writer = nil
	v.running = false
	v.finished = true
	v.mu.Unlock()

	if cam != nil {
		_ = cam.StopVideo()
	}
	if w != nil {
		if err := w.Close(); err != nil {
			v.mu.Lock()
			v.lastErr = err.Error()
			v.mu.Unlock()
		}
	}
}

// Stop ends the recording and waits for the file to be closed.
func (v *videoRecorder) Stop() VideoStatus {
	v.mu.Lock()
	cancel := v.cancel
	v.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Wait briefly for the loop to close the file, so the caller can act on a complete recording.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v.mu.Lock()
		done := !v.running
		v.mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return v.Status()
}

// Status reports progress.
func (v *videoRecorder) Status() VideoStatus {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := VideoStatus{
		Running: v.running, Path: v.path, Frames: v.frames,
		Dropped: v.dropped, LastError: v.lastErr, Finished: v.finished,
	}
	if !v.started.IsZero() {
		st.ElapsedSec = time.Since(v.started).Seconds()
		if st.ElapsedSec > 0 {
			st.FPS = float64(v.frames) / st.ElapsedSec
		}
	}
	return st
}

// colorIDFor maps a camera's Bayer pattern onto the SER colour code. A mono camera — which is what
// this observatory uses — is simply mono; getting this wrong on a colour camera would make every
// reader debayer with the wrong offset and produce false colour.
func colorIDFor(caps device.CameraCaps) ser.ColorID {
	if !caps.IsColor {
		return ser.ColorMono
	}
	switch caps.BayerPattern {
	case "RGGB":
		return ser.ColorBayerRGGB
	case "GRBG":
		return ser.ColorBayerGRBG
	case "GBRG":
		return ser.ColorBayerGBRG
	case "BGGR":
		return ser.ColorBayerBGGR
	default:
		return ser.ColorMono
	}
}

// --- HTTP -----------------------------------------------------------------------------------

// videoStart begins a SER recording. POST /video/start
func (s *Server) videoStart(w http.ResponseWriter, r *http.Request) {
	var opts VideoOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// The live view and a recording both drive the camera; running them together would fight over
	// the stream and drop frames from both.
	s.live.stop()
	if err := s.video.Start(opts); err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.video.Status())
}

// videoStop ends the recording and returns the finished file's stats. POST /video/stop
func (s *Server) videoStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.video.Stop())
}

// videoStatus reports progress. GET /video/status
func (s *Server) videoStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.video.Status())
}
