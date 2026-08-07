package capture

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/polaralign"
)

// Polar alignment from the live camera: the session that walks a user through it.
//
// The measurement itself is pure geometry and lives in internal/polaralign. What lives here is
// everything that makes it usable at three in the morning — driving the camera, deciding which frame is
// allowed to count, plate-solving, and publishing a state the browser can render.
//
// It sits in the engine rather than the device server for the same reason the centring loop does
// (see center.go): solving needs Siril, and only the engine has it.
//
// The mount is never commanded. Between frames the user turns the right-ascension axis by hand — with
// the clutch, the hand controller, whatever they have — and presses a button. The fit does not need to
// know how far it moved, only that it did, and that buys polar alignment on every mount ever made
// rather than only on the ones we can drive.

// PolarPhase is where the session has got to.
type PolarPhase string

// Modes a correction can come from.
const (
	// PolarMeasured came from a rotation of the right-ascension axis: arcminute class.
	PolarMeasured = "measured"
	// PolarRough came from one frame, assuming the telescope looks down the right-ascension axis:
	// polar-scope class, and only as true as that assumption.
	PolarRough = "rough"
)

const (
	PolarIdle      PolarPhase = "idle"
	PolarMeasuring PolarPhase = "measuring" // collecting frames along the rotation
	PolarSolved    PolarPhase = "solved"    // the axis is known; here is what to turn
	PolarAdjusting PolarPhase = "adjusting" // following along while the bolts turn
	PolarFailed    PolarPhase = "failed"
)

const (
	// defaultPoints is how many frames a measurement takes. Four rather than three because three
	// points always fit a circle exactly, so a three-frame measurement has no way to notice that
	// something was knocked between two of them.
	defaultPoints = 4
	// defaultExposureUs is long enough for a plate solve on a small refractor and short enough that
	// the whole procedure stays conversational.
	defaultExposureUs = 4_000_000
	// liveIntervalMs is the pause between live exposures during a session. The solve dominates.
	liveIntervalMs = 200
	// stepArcDeg is the rotation the user is asked for between frames. Four frames at 20° span 60°,
	// which is what buys sub-arcminute accuracy — see the measurements in polaralign/axis.go.
	stepArcDeg = 20
)

// frameWaitTimeout bounds how long a session waits for a live frame newer than the moment the user
// pressed the button. Beyond this the live loop has stalled and saying so beats hanging.
const frameWaitTimeout = 90 * time.Second

var (
	// ErrPolarBusy means an exposure or solve is already in flight.
	ErrPolarBusy = errors.New("the polar alignment session is busy")
	// ErrPolarNotRunning means the requested step needs a session that has been started.
	ErrPolarNotRunning = errors.New("no polar alignment session is running")
	// ErrPolarNoSolver means Siril is not available, so nothing can be measured.
	ErrPolarNoSolver = errors.New("plate solving is unavailable — Siril is not configured")
)

// PolarSample is one measured point, as the UI sees it.
type PolarSample struct {
	Index  int       `json:"index"`
	RADeg  float64   `json:"ra_deg"`
	DecDeg float64   `json:"dec_deg"`
	At     time.Time `json:"at"`
	// ScaleArcsecPx is reported per frame because it is the first thing to look at when a solve comes
	// back wrong: a plate scale nothing like the configured optics means the wrong focal length.
	ScaleArcsecPx float64 `json:"scale_arcsec_px"`
}

// PolarState is the whole session, in the shape the browser renders.
type PolarState struct {
	Phase  PolarPhase `json:"phase"`
	Step   int        `json:"step"`   // how many frames are in
	Points int        `json:"points"` // how many are wanted
	// StepArcDeg is how far to turn the axis before the next frame.
	StepArcDeg float64                `json:"step_arc_deg"`
	Samples    []PolarSample          `json:"samples"`
	Axis       *polaralign.Axis       `json:"axis,omitempty"`
	Correction *polaralign.Correction `json:"correction,omitempty"`
	Live       *polaralign.LiveState  `json:"live,omitempty"`
	// Pole is where the celestial pole and its guide star fall on the last solved frame. Present from
	// the first frame onward, because "where is the pole" is useful long before anything is measured —
	// it is what turns hunting for Polaris under a tripod into looking at a screen.
	Pole *polaralign.PoleView `json:"pole,omitempty"`
	// Mode says how the correction on offer was arrived at: "measured" from a rotation, or "rough" from
	// a single frame that assumed the telescope looks down the right-ascension axis. The UI must not
	// present the two as the same thing — they differ by two orders of magnitude in accuracy.
	Mode string `json:"mode,omitempty"`
	// Warnings are codes for the UI to translate, never English.
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
	Busy     bool     `json:"busy"`
	// Tracking records whether the drive was running when the session started, because that decides
	// whether the target rides the sky or stays put over the ground.
	Tracking bool `json:"tracking"`
}

// PolarOptions tune one session.
type PolarOptions struct {
	Site       polaralign.Site
	Points     int
	ExposureUs int64
	Gain       int64
	// NoRefraction turns off the refraction correction. Leaving it on is right; the option exists to
	// compare against tools that do not model it.
	NoRefraction bool
	FocalMM      float64
	PixelUm      float64
	// ScratchDir is where the solved frames go; empty means the OS temp dir. They are deleted with the
	// session.
	ScratchDir string
}

func (o PolarOptions) withDefaults() PolarOptions {
	out := o
	if out.Points < 3 {
		out.Points = defaultPoints
	}
	if out.ExposureUs <= 0 {
		out.ExposureUs = defaultExposureUs
	}
	return out
}

// PolarSession runs one alignment from start to finish.
type PolarSession struct {
	client *Client
	solver Solver

	mu      sync.Mutex
	opts    PolarOptions
	state   PolarState
	samples []polaralign.Sample
	live    *polaralign.Live
	scratch string
	// lastFrame is the most recent solved frame, kept so entering the adjust phase does not need to
	// take another one.
	lastFrame  polaralign.Frame
	lastCorr   polaralign.Correction
	haveLastFr bool

	subsMu sync.Mutex
	subs   map[chan PolarState]bool
}

// NewPolarSession builds a session. It holds no resources until Start.
func NewPolarSession(client *Client, solver Solver) *PolarSession {
	return &PolarSession{
		client: client,
		solver: solver,
		state:  PolarState{Phase: PolarIdle, Points: defaultPoints, StepArcDeg: stepArcDeg},
		subs:   map[chan PolarState]bool{},
	}
}

// Snapshot is the current state, safe to call at any time.
func (p *PolarSession) Snapshot() PolarState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Subscribe streams state changes. The returned function stops the stream, and is safe to call more
// than once — an SSE handler naturally unsubscribes both when its context ends and from a defer, and
// closing the channel twice would panic the whole engine over a browser tab being shut.
func (p *PolarSession) Subscribe() (<-chan PolarState, func()) {
	ch := make(chan PolarState, 8)
	p.subsMu.Lock()
	p.subs[ch] = true
	p.subsMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.subsMu.Lock()
			delete(p.subs, ch)
			p.subsMu.Unlock()
			close(ch)
		})
	}
}

// publish fans the state out. Slow subscribers drop updates rather than stall the session.
func (p *PolarSession) publish() {
	s := p.Snapshot()
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for ch := range p.subs {
		select {
		case ch <- s:
		default:
		}
	}
}

// Start begins a measurement and takes the first frame.
func (p *PolarSession) Start(ctx context.Context, opts PolarOptions) error {
	if err := p.begin(ctx, opts, PolarMeasuring); err != nil {
		return err
	}
	return p.captureAndSolve(ctx)
}

// Rough answers the whole question from ONE frame, by assuming the telescope is looking down the
// right-ascension axis — declination at its 90° index.
//
// That assumption is exactly the one a polar scope makes, and it buys the same thing: an alignment
// from a single glance, in ten seconds, with nothing to turn between frames. It is also only as good
// as the assumption, so the answer is marked PolarRough and carries the cone error it cannot see. Use
// it to get on the pole quickly; use Start to get on it properly.
func (p *PolarSession) Rough(ctx context.Context, opts PolarOptions) error {
	if err := p.begin(ctx, opts, PolarMeasuring); err != nil {
		return err
	}
	frame, err := p.solveFreshFrame(ctx)
	if err != nil {
		p.fail(err)
		return err
	}
	axis, ok := polaralign.RoughAxis(frame, p.opts.Site, p.fitOptions())
	if !ok {
		err := fmt.Errorf("the frame carries no usable plate solution")
		p.fail(err)
		return err
	}
	p.settle(frame, axis, PolarRough)
	return nil
}

// begin claims the session, prepares the camera and records whether the drive is running.
func (p *PolarSession) begin(ctx context.Context, opts PolarOptions, phase PolarPhase) error {
	if p.solver == nil {
		return ErrPolarNoSolver
	}
	o := opts.withDefaults()

	p.mu.Lock()
	if p.state.Busy {
		p.mu.Unlock()
		return ErrPolarBusy
	}
	p.cleanupLocked()
	scratch, err := os.MkdirTemp(o.ScratchDir, "astro-polar-*")
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.opts, p.scratch, p.samples, p.live, p.haveLastFr = o, scratch, nil, nil, false
	p.state = PolarState{
		Phase: phase, Points: o.Points, StepArcDeg: stepArcDeg, Busy: true,
	}
	p.mu.Unlock()
	p.publish()

	// Whether the drive is running changes what the target marker is pinned to, so it is read once here
	// rather than assumed. A mount we cannot reach is not an error: alignment does not need one.
	if st, err := p.client.Mount(ctx); err == nil {
		p.mu.Lock()
		p.state.Tracking = st.Mount.Tracking
		p.mu.Unlock()
	}

	if err := p.prepareCamera(ctx); err != nil {
		p.fail(err)
		return err
	}
	return nil
}

// settle publishes a finished measurement, however it was arrived at.
func (p *PolarSession) settle(frame polaralign.Frame, axis polaralign.Axis, mode string) {
	corr := polaralign.Correct(axis, p.opts.Site)

	view, haveView := polaralign.Locate(frame, p.opts.Site, p.fitOptions())

	p.mu.Lock()
	p.state.Phase = PolarSolved
	p.state.Busy = false
	p.state.Mode = mode
	if haveView {
		p.state.Pole = &view
	}
	p.state.Axis, p.state.Correction = &axis, &corr
	p.state.Warnings = axis.Warnings
	p.lastFrame, p.lastCorr, p.haveLastFr = frame, corr, true
	p.mu.Unlock()
	p.publish()
}

// Next records that the user has turned the axis, and takes the following frame.
func (p *PolarSession) Next(ctx context.Context) error {
	p.mu.Lock()
	switch {
	case p.state.Phase != PolarMeasuring:
		p.mu.Unlock()
		return ErrPolarNotRunning
	case p.state.Busy:
		p.mu.Unlock()
		return ErrPolarBusy
	}
	p.state.Busy = true
	p.mu.Unlock()
	p.publish()

	return p.captureAndSolve(ctx)
}

// Adjust moves to the live phase, where the user turns the bolts and watches the marker.
func (p *PolarSession) Adjust(ctx context.Context) error {
	p.mu.Lock()
	if p.state.Phase != PolarSolved {
		p.mu.Unlock()
		return ErrPolarNotRunning
	}
	if !p.haveLastFr {
		p.mu.Unlock()
		return fmt.Errorf("no solved frame to start from")
	}
	live, err := polaralign.NewLive(p.lastCorr, p.lastFrame, p.state.Tracking, p.fitOptions())
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.live = live
	p.state.Phase = PolarAdjusting
	p.state.Busy = true
	p.mu.Unlock()
	p.publish()

	return p.refresh(ctx)
}

// Refresh takes another frame during the adjust phase and updates the marker. The API calls it on a
// timer while the panel is open.
func (p *PolarSession) Refresh(ctx context.Context) error {
	p.mu.Lock()
	switch {
	case p.state.Phase != PolarAdjusting || p.live == nil:
		p.mu.Unlock()
		return ErrPolarNotRunning
	case p.state.Busy:
		p.mu.Unlock()
		return ErrPolarBusy
	}
	p.state.Busy = true
	p.mu.Unlock()
	p.publish()

	return p.refresh(ctx)
}

// Stop ends the session and clears its scratch frames.
func (p *PolarSession) Stop() {
	p.mu.Lock()
	p.cleanupLocked()
	p.state = PolarState{Phase: PolarIdle, Points: p.state.Points, StepArcDeg: stepArcDeg}
	p.samples, p.live, p.haveLastFr = nil, nil, false
	p.mu.Unlock()
	p.publish()
}

func (p *PolarSession) cleanupLocked() {
	if p.scratch != "" {
		_ = os.RemoveAll(p.scratch)
		p.scratch = ""
	}
}

func (p *PolarSession) fitOptions() polaralign.FitOptions {
	return polaralign.FitOptions{NoRefraction: p.opts.NoRefraction}
}

// prepareCamera sets the exposure and makes sure the live loop is running. The session works from the
// live stream rather than its own exposures so that the frame being measured is the frame on screen.
func (p *PolarSession) prepareCamera(ctx context.Context) error {
	if err := p.client.SetControl(ctx, device.ControlExposure, p.opts.ExposureUs); err != nil {
		return fmt.Errorf("set exposure: %w", err)
	}
	if p.opts.Gain > 0 {
		if err := p.client.SetControl(ctx, device.ControlGain, p.opts.Gain); err != nil {
			return fmt.Errorf("set gain: %w", err)
		}
	}
	if err := p.client.StartLive(ctx, liveIntervalMs); err != nil {
		return fmt.Errorf("start the live view: %w", err)
	}
	return nil
}

// captureAndSolve takes one frame, solves it, and either records a sample or finishes the measurement.
func (p *PolarSession) captureAndSolve(ctx context.Context) error {
	frame, err := p.solveFreshFrame(ctx)
	if err != nil {
		p.fail(err)
		return err
	}

	p.mu.Lock()
	idx := len(p.samples)
	cx, cy := float64(frame.WidthPx)/2, float64(frame.HeightPx)/2
	ra, dec := frame.WCS.PixToSky(cx, cy)
	p.samples = append(p.samples, polaralign.Sample{RADeg: ra, DecDeg: dec, At: frame.At})
	p.state.Samples = append(p.state.Samples, PolarSample{
		Index: idx + 1, RADeg: ra, DecDeg: dec, At: frame.At,
		ScaleArcsecPx: frame.WCS.ScaleArcsecPerPix(),
	})
	p.state.Step = len(p.samples)
	if view, ok := polaralign.Locate(frame, p.opts.Site, p.fitOptions()); ok {
		p.state.Pole = &view
	}
	p.lastFrame, p.haveLastFr = frame, true
	done := len(p.samples) >= p.opts.Points
	samples, site, opts := append([]polaralign.Sample(nil), p.samples...), p.opts.Site, p.fitOptions()
	p.state.Busy = false
	p.mu.Unlock()

	if !done {
		p.publish()
		return nil
	}

	axis, err := polaralign.FitAxis(samples, site, opts)
	if err != nil {
		p.fail(err)
		return err
	}
	p.settle(frame, axis, PolarMeasured)
	return nil
}

// refresh takes one frame during the adjust phase and updates the marker.
func (p *PolarSession) refresh(ctx context.Context) error {
	frame, err := p.solveFreshFrame(ctx)
	if err != nil {
		// A single unsolvable frame during adjustment is a cloud, not a failure: keep the last good
		// marker on screen and try again on the next tick.
		p.mu.Lock()
		p.state.Busy = false
		p.state.Error = err.Error()
		p.mu.Unlock()
		p.publish()
		return err
	}

	p.mu.Lock()
	live := p.live
	p.mu.Unlock()
	if live == nil {
		return ErrPolarNotRunning
	}
	st, err := live.Update(frame)

	p.mu.Lock()
	p.state.Busy = false
	if err != nil {
		p.state.Error = err.Error()
	} else {
		p.state.Error = ""
		p.state.Live = &st
		if view, ok := polaralign.Locate(frame, p.opts.Site, p.fitOptions()); ok {
			p.state.Pole = &view
		}
		p.lastFrame, p.haveLastFr = frame, true
	}
	p.mu.Unlock()
	p.publish()
	return err
}

// solveFreshFrame waits for a live frame taken AFTER this call, writes it out and solves it.
//
// The wait is the whole point. The frame sitting in memory when the user finishes turning the mount was
// exposed while it was still moving, or before it moved at all — solving that one would put two
// measurements at the same place and quietly ruin the fit.
func (p *PolarSession) solveFreshFrame(ctx context.Context) (polaralign.Frame, error) {
	p.mu.Lock()
	scratch, n := p.scratch, len(p.samples)
	hint := p.solveHintLocked()
	p.mu.Unlock()
	if scratch == "" {
		return polaralign.Frame{}, ErrPolarNotRunning
	}

	after, err := p.currentSeq(ctx)
	if err != nil {
		return polaralign.Frame{}, err
	}

	path := filepath.Join(scratch, fmt.Sprintf("polar_%02d_%d.fit", n+1, time.Now().UnixNano()))
	saved, err := p.waitForFrameAfter(ctx, after, path)
	if err != nil {
		return polaralign.Frame{}, err
	}

	res, err := p.solver.Solve(ctx, saved.Path, hint)
	if err != nil {
		return polaralign.Frame{}, fmt.Errorf("the frame could not be plate-solved: %w", err)
	}
	at, err := time.Parse(time.RFC3339Nano, saved.StartedAt)
	if err != nil {
		return polaralign.Frame{}, fmt.Errorf("the frame carries no usable timestamp: %w", err)
	}
	// Mid-exposure, not the start: hour angle is what the fit works in, and half of a long sub is
	// minutes of sky rotation.
	mid := at.Add(time.Duration(saved.ExposureUs/2) * time.Microsecond)

	return polaralign.Frame{
		WCS: res.WCS, WidthPx: saved.Width, HeightPx: saved.Height, At: mid,
	}, nil
}

// currentSeq reads the live loop's sequence number, so the next save can be required to beat it.
func (p *PolarSession) currentSeq(ctx context.Context) (int64, error) {
	st, err := p.client.LiveStatus(ctx)
	if err != nil {
		return 0, fmt.Errorf("the live view is not reachable: %w", err)
	}
	if !st.Running {
		if err := p.client.StartLive(ctx, liveIntervalMs); err != nil {
			return 0, fmt.Errorf("start the live view: %w", err)
		}
	}
	return st.Seq, nil
}

// waitForFrameAfter polls the device server until it writes a frame newer than seq.
func (p *PolarSession) waitForFrameAfter(ctx context.Context, seq int64, path string) (LiveFrame, error) {
	deadline := time.Now().Add(frameWaitTimeout)
	for {
		saved, err := p.client.SaveLiveFrame(ctx, SaveRequest{
			Path: path, Type: "light", Object: "polar-align",
			FocalMM: p.opts.FocalMM,
		})
		if err == nil && saved.Seq > seq {
			return saved, nil
		}
		if err != nil && ctx.Err() != nil {
			return LiveFrame{}, ctx.Err()
		}
		if time.Now().After(deadline) {
			if err != nil {
				return LiveFrame{}, fmt.Errorf("no live frame arrived: %w", err)
			}
			return LiveFrame{}, errors.New("the live view stopped producing frames")
		}
		select {
		case <-ctx.Done():
			return LiveFrame{}, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// solveHintLocked is where to tell the solver to look. The previous frame is the best hint there is —
// the mount has been turned since, but by tens of degrees at most, which still turns an all-sky search
// into a couple of seconds. Caller holds the lock.
func (p *PolarSession) solveHintLocked() platesolve.Hint {
	hint := platesolve.Hint{FocalMM: p.opts.FocalMM, PixelUm: p.opts.PixelUm}
	if n := len(p.samples); n > 0 {
		last := p.samples[n-1]
		hint.RADeg, hint.DecDeg, hint.HasHint = last.RADeg, last.DecDeg, true
	}
	return hint
}

// fail records a terminal error and tells the UI.
func (p *PolarSession) fail(err error) {
	p.mu.Lock()
	p.state.Phase = PolarFailed
	p.state.Busy = false
	p.state.Error = err.Error()
	p.mu.Unlock()
	p.publish()
}
