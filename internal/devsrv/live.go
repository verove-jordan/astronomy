package devsrv

import (
	"context"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/focus"
)

// The live view: a loop that keeps exposing and keeps the newest frame in memory, so the browser
// can watch through the telescope while framing and focusing.
//
// Frames are served in the engine's existing 16-bit preview format, which means the whole client
// side already exists: the browser decodes it with the same code it uses for file previews,
// stretches it locally with a LUT (so the black/white sliders are instant and never hit the
// network), and pans/zooms it with the same viewer as everything else.

// nowFunc is indirected for tests.
var nowFunc = time.Now

// liveView owns the acquisition loop and the newest frame.
type liveView struct {
	srv *Server

	mu       sync.RWMutex
	running  bool
	cancel   context.CancelFunc
	frame    *device.Frame
	stats    *FrameStats
	seq      int64
	lastErr  string
	interval time.Duration
	// expEnds/expUs describe the exposure CURRENTLY in flight. The browser needs them to count down
	// the wait: without them a long sub looks like a frozen screen, and there is no way to tell "still
	// integrating" from "the camera has stopped".
	expEnds time.Time
	expUs   int64

	subsMu sync.Mutex
	subs   map[chan struct{}]bool

	// meter carries the focus history across frames — the only way to answer "which way do I
	// turn?", since one frame cannot distinguish inside from outside focus.
	meter *focus.Meter
}

func newLiveView(s *Server) *liveView {
	return &liveView{srv: s, subs: map[chan struct{}]bool{}, meter: focus.NewMeter()}
}

// ResetFocus forgets the focus history. A filter change or a different camera shifts focus, so the
// session's "best so far" no longer means anything.
func (l *liveView) resetFocus() { l.meter.Reset() }

// start begins looping exposures. Restarting an already-running loop just re-applies the interval.
func (l *liveView) start(interval time.Duration) error {
	l.mu.Lock()
	if l.running {
		l.interval = interval
		l.mu.Unlock()
		return nil
	}
	cam := l.srv.currentCamera()
	if cam == nil {
		l.mu.Unlock()
		return device.ErrNotConnected
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.running = true
	l.cancel = cancel
	l.interval = interval
	l.mu.Unlock()

	go l.loop(ctx, cam)
	return nil
}

func (l *liveView) stop() {
	l.mu.Lock()
	cancel := l.cancel
	l.running = false
	l.cancel = nil
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *liveView) isRunning() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.running
}

// loop exposes continuously until cancelled. It uses still exposures rather than video mode: the
// same code path then serves a 32 µs framing exposure and a 30-second focus check, and long
// exposures stay cancellable.
func (l *liveView) loop(ctx context.Context, cam device.Camera) {
	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		// Published BEFORE the exposure starts, so the countdown is live for its whole duration rather
		// than only appearing once the frame has already arrived.
		l.beginExposure(cam)
		l.notify()

		frame, err := l.exposeOnce(ctx, cam)
		if err != nil {
			l.mu.Lock()
			l.lastErr = err.Error()
			l.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			// Back off a little on error rather than hammering a sick device.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		stats := measureFrame(frame)
		l.attachFocus(frame, stats)
		l.mu.Lock()
		l.frame = frame
		l.stats = stats
		l.seq++
		l.lastErr = ""
		interval := l.interval
		// The frame has landed; the next exposure starts after the pause, so that is when the next one
		// is due.
		l.expEnds = nowFunc().Add(interval)
		l.mu.Unlock()
		l.notify()

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// exposeOnce runs one exposure to completion, polling rather than blocking so a long exposure can
// still be cancelled promptly.
func (l *liveView) exposeOnce(ctx context.Context, cam device.Camera) (*device.Frame, error) {
	if err := cam.StartExposure(ctx, false); err != nil {
		return nil, err
	}
	for {
		state, err := cam.ExposureState()
		if err != nil {
			return nil, err
		}
		switch state {
		case device.ExposureSuccess:
			return cam.Download(ctx)
		case device.ExposureFailed:
			_ = cam.AbortExposure()
			return nil, errFailedExposure
		}
		select {
		case <-ctx.Done():
			_ = cam.AbortExposure()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// latest returns the newest frame and its stats (nil before the first one lands).
// beginExposure stamps the deadline for the exposure about to be taken.
func (l *liveView) beginExposure(cam device.Camera) {
	var us int64
	if c, ok := cam.Control(device.ControlExposure); ok {
		us = c.Value
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expUs = us
	// The readout and transfer add to the wall-clock wait, so the deadline is the exposure plus the
	// configured pause — a countdown that hits zero and then sits there is worse than one that is
	// slightly generous.
	l.expEnds = nowFunc().Add(time.Duration(us)*time.Microsecond + l.interval)
}

// exposureDeadline reports when the frame in flight is expected, and its exposure length.
func (l *liveView) exposureDeadline() (time.Time, int64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.expEnds, l.expUs
}

func (l *liveView) latest() (*device.Frame, *FrameStats, int64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.frame, l.stats, l.seq
}

// subscribe returns a channel that ticks whenever a new frame lands, plus its unsubscribe.
func (l *liveView) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.subsMu.Lock()
	l.subs[ch] = true
	l.subsMu.Unlock()
	return ch, func() {
		l.subsMu.Lock()
		delete(l.subs, ch)
		l.subsMu.Unlock()
		close(ch)
	}
}

// notify wakes every subscriber without blocking: a slow client misses intermediate frames rather
// than stalling the acquisition loop, which is the right trade for a live view.
func (l *liveView) notify() {
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	for ch := range l.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Server) currentCamera() device.Camera {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.camera
}

// errFailedExposure is returned when the driver reports a failed exposure.
var errFailedExposure = errorString("the camera reported a failed exposure")

type errorString string

func (e errorString) Error() string { return string(e) }

// attachFocus measures how sharp the frame is and folds the verdict into its statistics. It runs on
// the acquisition goroutine, on a centre crop, so it costs milliseconds rather than competing with
// the exposure it just finished.
func (l *liveView) attachFocus(frame *device.Frame, stats *FrameStats) {
	if frame == nil || stats == nil || len(frame.Pix) == 0 {
		return
	}
	cfg := l.srv.cfg
	opts := focus.Options{}
	if cfg != nil {
		opts.PixelUm = cfg.PixelSizeUm
		opts.FocalMM = cfg.FocalLenMM
		opts.ApertureMM = cfg.ApertureMM
		if cfg.FocalLenMM > 0 && cfg.PixelSizeUm > 0 {
			opts.ScaleArcsecPx = 206.264806 * cfg.PixelSizeUm / cfg.FocalLenMM * float64(maxInt(1, frame.Bin))
		}
	}
	res := l.meter.Measure(frame.Pix, frame.Width, frame.Height, opts)
	stats.Focus = &FocusStats{
		Score: round2(res.Score), HFDPx: round2(res.HFDPx), HFDArcsec: round2(res.HFDArcsec),
		Stars: res.Stars, Saturated: res.Saturated, Reliable: res.Reliable,
		DistanceUm: round2(res.DistanceUm), Turns: round2(res.Turns),
		Advice: res.Advice, BestHFDPx: round2(res.BestHFDPx),
		TiltCorners: roundAll(res.TiltCorners),
	}
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func roundAll(vals []float64) []float64 {
	if len(vals) == 0 {
		return nil
	}
	out := make([]float64, len(vals))
	for i, v := range vals {
		out[i] = round2(v)
	}
	return out
}
