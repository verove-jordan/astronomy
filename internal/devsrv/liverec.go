package devsrv

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Keeping the frames the live view is already taking.
//
// Deliberately NOT videoRecorder. That one puts the camera into VIDEO mode and streams one SER file,
// which is how planets are shot; this saves what the preview is already producing, as ordinary FITS
// the rest of the pipeline can read, without changing the camera's mode or restarting anything. It
// is for the case that has no other answer: something good is on screen NOW and the decision to keep
// it comes after the frame, not before.
//
// # Why the cap keeps a BURST rather than every Nth frame
//
// At microsecond exposures the loop delivers far faster than a disk should be asked to swallow, so
// at most maxFPS frames a second are kept. WHICH ones are dropped decides what the recording is
// worth. Taking every Nth spreads the survivors evenly across the second — and evenly spaced frames
// are the least useful ones there are, because seeing decorrelates between them: they neither stack
// cleanly nor show a real drift, they show aliasing. Consecutive frames sample the same instant of
// atmosphere. So each second's budget is spent on the first frames of that second, back to back, and
// the remainder are skipped. A burst, not a comb.

const (
	// liveRecordDefaultFPS caps the save rate when the caller names none.
	liveRecordDefaultFPS = 15.0
	// liveRecordIdleCheck is how often the loop looks up from the frame channel. It only matters when
	// no frames are arriving at all — a live view that was stopped underneath the recording.
	liveRecordIdleCheck = time.Second
	// liveRecordDefaultMaxFrames bounds a recording nobody stops. Somebody who starts one and walks
	// to the scope must not come back to a full disk — and a run that wants more says so.
	liveRecordDefaultMaxFrames = 600
)

// LiveRecordOptions start a recording.
type LiveRecordOptions struct {
	// Dir is where the frames land. Empty means a timestamped folder under the configured output
	// directory, so the button works with nothing filled in.
	Dir string `json:"dir"`
	// MaxFPS caps how many frames a second are kept; MaxFrames and MaxSeconds stop the recording on
	// their own. Zero takes the defaults above (MaxSeconds zero means "no time limit").
	MaxFPS     float64 `json:"max_fps"`
	MaxFrames  int     `json:"max_frames"`
	MaxSeconds float64 `json:"max_seconds"`

	// The header metadata only the caller knows. Type defaults to "light".
	Type      string  `json:"type"`
	Filter    string  `json:"filter"`
	Object    string  `json:"object"`
	Telescope string  `json:"telescope"`
	FocalMM   float64 `json:"focal_mm"`
}

func (o LiveRecordOptions) withDefaults() LiveRecordOptions {
	out := o
	if out.MaxFPS <= 0 {
		out.MaxFPS = liveRecordDefaultFPS
	}
	if out.MaxFrames <= 0 {
		out.MaxFrames = liveRecordDefaultMaxFrames
	}
	if out.Type == "" {
		out.Type = "light"
	}
	return out
}

// LiveRecordStatus is what the button renders from.
type LiveRecordStatus struct {
	Running bool   `json:"running"`
	Dir     string `json:"dir,omitempty"`
	Saved   int    `json:"saved"`
	// Skipped were dropped by the rate cap; Missed went past while a frame was being written, which
	// means the disk is the limit rather than the cap.
	Skipped    int     `json:"skipped"`
	Missed     int     `json:"missed"`
	MaxFrames  int     `json:"max_frames,omitempty"`
	MaxFPS     float64 `json:"max_fps,omitempty"`
	ElapsedSec float64 `json:"elapsed_sec"`
	LastPath   string  `json:"last_path,omitempty"`
	LastError  string  `json:"last_error,omitempty"`
	Finished   bool    `json:"finished"`
}

// liveRecorder owns one recording.
type liveRecorder struct {
	srv *Server

	mu           sync.Mutex
	running      bool
	finished     bool
	cancel       context.CancelFunc
	opts         LiveRecordOptions
	dir          string
	saved        int
	skipped      int
	missedFrames int
	started      time.Time
	lastPath     string
	lastErr      string
}

func newLiveRecorder(s *Server) *liveRecorder { return &liveRecorder{srv: s} }

// Start begins saving. The FIRST frame kept is the one already on screen, because that is the one
// the user was looking at when they decided to keep it — waiting for the next would drop exactly the
// picture that prompted the click.
func (l *liveRecorder) Start(opts LiveRecordOptions) (LiveRecordStatus, error) {
	opts = opts.withDefaults()

	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return LiveRecordStatus{}, fmt.Errorf("%w: a recording is already running", device.ErrBusy)
	}
	l.mu.Unlock()

	// Nobody names the filter when they press a button, and the wheel already knows what is in the
	// beam. Without this every recorded frame was nameless — no FILTER card, no filter- token — which
	// for a mono rig is the one piece of metadata the stack cannot be assembled without.
	if opts.Filter == "" {
		opts.Filter = l.srv.currentFilterName()
	}
	if !l.srv.live.isRunning() {
		return LiveRecordStatus{}, fmt.Errorf("%w: the live view is not running", device.ErrNotConnected)
	}
	dir := opts.Dir
	if dir == "" {
		dir = filepath.Join(l.srv.liveRecordRoot(), time.Now().Format("live_20060102_150405"))
	}
	if !filepath.IsAbs(dir) {
		return LiveRecordStatus{}, fmt.Errorf("an absolute folder is required, got %q", dir)
	}
	if err := ensureWritableDir(dir); err != nil {
		return LiveRecordStatus{}, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	l.mu.Lock()
	l.running, l.finished = true, false
	l.cancel, l.opts, l.dir = cancel, opts, dir
	l.saved, l.skipped, l.missedFrames = 0, 0, 0
	l.lastPath, l.lastErr = "", ""
	l.started = time.Now()
	l.mu.Unlock()

	go l.loop(ctx)
	return l.Status(), nil
}

// Stop ends the recording. Stopping one that is not running is not an error: the button is a toggle
// and a double click must not become a failure.
func (l *liveRecorder) Stop() LiveRecordStatus {
	l.mu.Lock()
	cancel := l.cancel
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return l.Status()
}

func (l *liveRecorder) Status() LiveRecordStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := LiveRecordStatus{
		Running: l.running, Dir: l.dir, Saved: l.saved,
		Skipped: l.skipped, Missed: l.missedFrames,
		MaxFrames: l.opts.MaxFrames, MaxFPS: l.opts.MaxFPS,
		LastPath: l.lastPath, LastError: l.lastErr, Finished: l.finished,
	}
	if !l.started.IsZero() {
		st.ElapsedSec = time.Since(l.started).Seconds()
	}
	return st
}

// loop keeps frames until it is stopped or hits a limit.
func (l *liveRecorder) loop(ctx context.Context) {
	defer func() {
		l.mu.Lock()
		l.running, l.finished, l.cancel = false, true, nil
		l.mu.Unlock()
	}()

	frames, unsubscribe := l.srv.live.subscribe()
	defer unsubscribe()

	l.mu.Lock()
	opts, deadline := l.opts, time.Time{}
	if opts.MaxSeconds > 0 {
		deadline = l.started.Add(time.Duration(opts.MaxSeconds * float64(time.Second)))
	}
	l.mu.Unlock()

	// budget is the burst: how many frames may still be kept in the second that began at window.
	perSecond := int(opts.MaxFPS)
	if perSecond < 1 {
		perSecond = 1
	}
	window, kept := time.Now(), 0
	lastSeq := int64(-1)

	for {
		frame, _, seq := l.srv.live.latest()
		if frame != nil && seq != lastSeq {
			// Frames that went past while the last one was being written are MISSED, not skipped: the
			// disk was the limit, not the cap. Counting them apart is the only way to tell "I asked for
			// 15 a second and got 15" from "the disk cannot keep up", which are different problems.
			if lastSeq >= 0 && seq > lastSeq+1 {
				l.missed(int(seq - lastSeq - 1))
			}
			lastSeq = seq
			if now := time.Now(); now.Sub(window) >= time.Second {
				window, kept = now, 0
			}
			if kept < perSecond {
				kept++
				if done := l.keep(frame, opts); done {
					return
				}
			} else {
				l.mu.Lock()
				l.skipped++
				l.mu.Unlock()
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return
		}
		// A live view that has stopped never ticks again, so waiting on the channel alone would leave
		// this goroutine parked for the rest of the process. Wake anyway and check.
		select {
		case <-ctx.Done():
			return
		case <-frames:
		case <-time.After(liveRecordIdleCheck):
			if !l.srv.live.isRunning() {
				l.fail("the live view stopped")
				return
			}
		}
	}
}

// missed records frames that passed while one was being written.
func (l *liveRecorder) missed(n int) {
	l.mu.Lock()
	l.missedFrames += n
	l.mu.Unlock()
}

// keep writes one frame and reports whether the recording is finished.
func (l *liveRecorder) keep(frame *device.Frame, opts LiveRecordOptions) bool {
	cam := l.srv.currentCamera()
	if cam == nil {
		l.fail("the camera went away")
		return true
	}
	l.mu.Lock()
	n, dir := l.saved+1, l.dir
	l.mu.Unlock()

	// The name mirrors what writeFrame is about to record in the header, so a file that loses its
	// header is still classified correctly by inspect. FileName owns that format; this only supplies
	// the fields it reads.
	meta := device.FrameMeta{
		Type: opts.Type, Filter: opts.Filter,
		ExposureUs: frame.ExposureUs, Gain: frame.Gain, Bin: frame.Bin,
		TempMilliC: frame.TempMilliC, HasTemp: frame.HasTemp,
		StartedAt: frame.StartedAt,
	}
	path := filepath.Join(dir, meta.FileName(n))

	if _, err := writeFrame(frame, cam.Caps(), saveRequest{
		Path: path, Type: opts.Type, Filter: opts.Filter, Object: opts.Object,
		Telescope: opts.Telescope, FocalMM: opts.FocalMM,
	}); err != nil {
		l.fail(err.Error())
		return true
	}

	l.mu.Lock()
	l.saved, l.lastPath = n, path
	done := n >= opts.MaxFrames
	l.mu.Unlock()
	return done
}

func (l *liveRecorder) fail(msg string) {
	l.mu.Lock()
	l.lastErr = msg
	l.mu.Unlock()
}

// liveRecordRoot is where an unnamed recording goes: a folder under the configured output directory,
// made absolute because the config's default is relative and the recorder refuses a relative path —
// a capture written somewhere that depends on the process's working directory is a capture nobody
// finds again.
func (s *Server) liveRecordRoot() string {
	dir := "output"
	if s.cfg != nil && s.cfg.OutputDir != "" {
		dir = s.cfg.OutputDir
	}
	abs, err := filepath.Abs(filepath.Join(dir, "live"))
	if err != nil {
		return filepath.Join(dir, "live")
	}
	return abs
}

// --- HTTP ------------------------------------------------------------------------------------

// liveRecordStart begins keeping frames. POST /live/record/start
func (s *Server) liveRecordStart(w http.ResponseWriter, r *http.Request) {
	var opts LiveRecordOptions
	_ = decodeBody(w, r, &opts) // an empty body means "every default", which is what the button sends
	st, err := s.liveRec.Start(opts)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// liveRecordStop ends it. POST /live/record/stop
func (s *Server) liveRecordStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.liveRec.Stop())
}

// liveRecordStatus is what the button polls. GET /live/record
func (s *Server) liveRecordStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.liveRec.Status())
}

// currentFilterName is what is actually in the beam right now.
//
// A wheel that is absent, moving or unnamed contributes NOTHING rather than a guess: a frame labelled
// with the filter it was not shot through is worse than one labelled with none, because the first
// silently joins the wrong stack and the second is merely awkward.
func (s *Server) currentFilterName() string {
	wheel := s.currentWheel()
	if wheel == nil {
		return ""
	}
	st, err := wheel.State()
	if err != nil || st.Moving || st.Position < 1 || st.Position > len(st.Names) {
		return ""
	}
	return strings.TrimSpace(st.Names[st.Position-1])
}
