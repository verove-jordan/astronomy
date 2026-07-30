package devsrv

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guidestar"
	"github.com/verove-jordan/astronomy/internal/pec"
)

// The periodic-error training run: watch one star for the best part of an hour, and turn what it did
// into a table the mount can replay.
//
// It lives in the device process, next to liveView and the flat-exposure search, because it needs the
// camera and the mount in the same breath and at a cadence an HTTP round trip per frame would spoil.
// It owns both while it runs: the live view is stopped, and the mount status endpoint is served from
// this session's cache, because the phase clock wants the serial port several times a second and
// every mount query goes through the same single-command-in-flight mutex.
//
// # Whole frames, not a sub-window
//
// A small ROI around the star would be quicker to download, and the temptation is obvious. It is
// declined: the ROI origin then has to be added back into every centroid, that arithmetic is exactly
// the kind that is silently wrong by a constant, and the simulator ignores the ROI origin entirely —
// so the sim would report a confident green for code that was broken on real hardware. At one frame
// per second or two, a full frame costs nothing that matters.

// PECPhase is where a training run has got to.
type PECPhase string

const (
	PECIdle        PECPhase = "idle"
	PECCalibrating PECPhase = "calibrating"
	PECMeasuring   PECPhase = "measuring"
	PECWriting     PECPhase = "writing"
	PECVerifying   PECPhase = "verifying"
	PECDone        PECPhase = "done"
	PECFailed      PECPhase = "failed"
)

// PECTrainRequest configures a run.
type PECTrainRequest struct {
	// ExposureSec is the sampling exposure. About a second is right: long enough that seeing averages
	// out rather than being chased, short enough for several samples per worm bin.
	ExposureSec float64 `json:"exposure_sec"`
	// Cycles is how many worm revolutions to watch. Each is eight minutes on an AVX; three is the
	// least that can say anything about repeatability, five is comfortable.
	Cycles float64 `json:"cycles"`
	// Bin is the sensor binning used for sampling.
	Bin int `json:"bin"`
	// DriftSec is how long the drive is stopped for the geometry calibration.
	DriftSec float64 `json:"drift_sec"`
	// Write, when false, measures and reports without ever touching the mount's table.
	Write bool `json:"write"`
}

func (r PECTrainRequest) withDefaults() PECTrainRequest {
	if r.ExposureSec <= 0 {
		r.ExposureSec = 1
	}
	if r.Cycles <= 0 {
		r.Cycles = 4
	}
	if r.Bin <= 0 {
		r.Bin = 1
	}
	if r.DriftSec <= 0 {
		r.DriftSec = 8
	}
	return r
}

// PECCalibration maps pixels onto the mount's RA axis.
type PECCalibration struct {
	// AxisArcsecPerPx is how much axis rotation one pixel of star motion represents. It absorbs the
	// cos(dec) factor by construction — the sky moves cos(dec) times as far as the axis does, and
	// this was measured against the sky — so no declination appears anywhere downstream.
	AxisArcsecPerPx float64 `json:"axis_arcsec_per_px"`
	// UnitX and UnitY point along the direction a POSITIVE axis rotation moves the star.
	UnitX float64 `json:"unit_x"`
	UnitY float64 `json:"unit_y"`
	// DriftPx is how far the star actually moved during the calibration, kept because a short drift
	// makes everything downstream imprecise and the user should be able to see why.
	DriftPx float64 `json:"drift_px"`
}

// AxisArcsec converts a star offset in pixels into an axis error.
func (c PECCalibration) AxisArcsec(dx, dy float64) float64 {
	return c.AxisArcsecPerPx * (dx*c.UnitX + dy*c.UnitY)
}

// PECTrainState is the whole visible state of a run.
type PECTrainState struct {
	Phase   PECPhase `json:"phase"`
	Message string   `json:"message,omitempty"`
	Error   string   `json:"error,omitempty"`

	Samples     int             `json:"samples"`
	Cycles      float64         `json:"cycles"`
	Progress    float64         `json:"progress"`
	Lost        int             `json:"lost"`
	StarSNR     float64         `json:"star_snr,omitempty"`
	StarHFD     float64         `json:"star_hfd,omitempty"`
	Calibration *PECCalibration `json:"calibration,omitempty"`
	Report      *PECReport      `json:"report,omitempty"`

	// Backup is the curve found in the mount before anything was written. It is surfaced, not just
	// kept, because it may be the only copy of an hour somebody spent with a hand controller — and
	// the device process does not survive a crash, so the UI persists it engine-side.
	Backup  []int `json:"backup,omitempty"`
	Written []int `json:"written,omitempty"`

	Verify      *PECReport       `json:"verify,omitempty"`
	Improvement *pec.Improvement `json:"improvement,omitempty"`
	Reverted    bool             `json:"reverted,omitempty"`

	StartedAtMs  int64 `json:"started_at_ms,omitempty"`
	FinishedAtMs int64 `json:"finished_at_ms,omitempty"`
}

// pecSession owns a training run.
type pecSession struct {
	srv *Server

	mu      sync.RWMutex
	running bool
	cancel  context.CancelFunc
	state   PECTrainState
	samples []pec.Sample
	// mountCache lets the ordinary mount status endpoint answer without touching the serial port
	// while this session is using it several times a second.
	mountCache    *device.MountState
	mountCachedAt time.Time

	subsMu sync.Mutex
	subs   map[chan struct{}]bool
}

func newPECSession(s *Server) *pecSession {
	return &pecSession{srv: s, subs: map[chan struct{}]bool{}, state: PECTrainState{Phase: PECIdle}}
}

var errPECBusy = errors.New("a periodic-error training run is already going")

// start kicks off a run in its own goroutine.
func (p *pecSession) start(req PECTrainRequest) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return errPECBusy
	}
	mount := p.srv.pecMount()
	if mount == nil {
		p.mu.Unlock()
		return errNoPEC
	}
	if p.srv.currentCamera() == nil {
		p.mu.Unlock()
		return device.ErrNotConnected
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.running, p.cancel = true, cancel
	p.samples = nil
	p.state = PECTrainState{Phase: PECCalibrating, StartedAtMs: nowFunc().UnixMilli()}
	p.mu.Unlock()

	// The training run drives the camera itself, so the live view must let go of it.
	p.srv.live.stop()

	go p.run(ctx, req.withDefaults(), mount)
	return nil
}

func (p *pecSession) stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *pecSession) isRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running
}

func (p *pecSession) snapshot() PECTrainState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// cachedMountState serves the mount status endpoint while a run owns the port.
func (p *pecSession) cachedMountState() (device.MountState, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.running || p.mountCache == nil {
		return device.MountState{}, false
	}
	return *p.mountCache, true
}

func (p *pecSession) setPhase(phase PECPhase, format string, args ...any) {
	p.mu.Lock()
	p.state.Phase = phase
	p.state.Message = fmt.Sprintf(format, args...)
	p.mu.Unlock()
	p.notify()
}

func (p *pecSession) fail(err error) {
	p.mu.Lock()
	p.state.Phase = PECFailed
	p.state.Error = err.Error()
	p.state.FinishedAtMs = nowFunc().UnixMilli()
	p.mu.Unlock()
	p.notify()
}

func (p *pecSession) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	p.subsMu.Lock()
	p.subs[ch] = true
	p.subsMu.Unlock()
	return ch, func() {
		p.subsMu.Lock()
		delete(p.subs, ch)
		p.subsMu.Unlock()
		close(ch)
	}
}

func (p *pecSession) notify() {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	for ch := range p.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// run is the state machine: prepare, calibrate, measure, then optionally write and verify.
func (p *pecSession) run(ctx context.Context, req PECTrainRequest, mount device.PECMount) {
	defer func() {
		p.mu.Lock()
		p.running, p.cancel = false, nil
		if p.state.FinishedAtMs == 0 {
			p.state.FinishedAtMs = nowFunc().UnixMilli()
		}
		p.mu.Unlock()
		p.notify()
	}()

	caps, err := p.prepare(ctx, mount)
	if err != nil {
		p.fail(err)
		return
	}
	geom := pec.Geometry{
		Bins:            caps.Bins,
		WormPeriodSec:   caps.WormPeriodSec,
		LSBArcsecPerSec: caps.LSBArcsecPerSec,
	}

	cal, star, err := p.calibrate(ctx, req)
	if err != nil {
		p.fail(err)
		return
	}
	p.mu.Lock()
	p.state.Calibration = &cal
	p.mu.Unlock()

	if err := p.measure(ctx, req, mount, geom, cal, star); err != nil {
		p.fail(err)
		return
	}

	report, err := p.analyse(geom, req)
	if err != nil {
		p.fail(err)
		return
	}
	p.mu.Lock()
	p.state.Report = report
	p.mu.Unlock()

	if !req.Write {
		p.setPhase(PECDone, "measured only — nothing was written to the mount")
		return
	}
	if err := p.writeAndVerify(ctx, req, mount, geom, cal, star, report); err != nil {
		p.fail(err)
		return
	}
}

// prepare asserts every precondition and puts the mount into a state the measurement can trust.
func (p *pecSession) prepare(ctx context.Context, mount device.PECMount) (device.PECCaps, error) {
	// Both of these are sent unconditionally rather than checked first: the protocol offers no way to
	// ask whether playback or a recording is running, and both would silently ruin the run — playback
	// by making us measure the residual instead of the error, a hand-controller recording by
	// overwriting the user's table with whatever it sees while we watch.
	if err := mount.PECRecordStop(ctx); err != nil {
		return device.PECCaps{}, err
	}
	if err := mount.PECPlayback(ctx, false); err != nil {
		return device.PECCaps{}, err
	}

	st, err := mount.PECStatus(ctx)
	if err != nil {
		return device.PECCaps{}, err
	}
	if !st.Indexed {
		p.setPhase(PECCalibrating, "finding the worm's index mark — this moves the mount")
		if err := mount.PECSeekIndex(ctx); err != nil {
			return device.PECCaps{}, err
		}
	}
	return mount.PECCaps(ctx)
}

// calibrate works out what a pixel is worth, by stopping the drive and watching the sky go by.
//
// This is measured rather than derived from the focal length and pixel size for two reasons. It gives
// the DIRECTION of the RA axis on the sensor, which depends on how the camera happens to be rotated,
// and it folds in cos(dec) on its own — the sky moves cos(dec) times as far as the axis turns, and
// this measurement is of the sky. Nothing downstream then has to remember a declination.
func (p *pecSession) calibrate(ctx context.Context, req PECTrainRequest) (PECCalibration, guidestar.Star, error) {
	p.setPhase(PECCalibrating, "choosing a guide star")
	cam := p.srv.currentCamera()
	if cam == nil {
		return PECCalibration{}, guidestar.Star{}, device.ErrNotConnected
	}
	mount := p.srv.currentMount()
	if mount == nil {
		return PECCalibration{}, guidestar.Star{}, device.ErrNotConnected
	}

	first, err := p.grabStar(ctx, req, nil)
	if err != nil {
		return PECCalibration{}, guidestar.Star{}, fmt.Errorf("choosing a guide star: %w", err)
	}

	p.setPhase(PECCalibrating, "stopping the drive to measure the image scale")
	if err := mount.SetTracking(ctx, false, ""); err != nil {
		return PECCalibration{}, guidestar.Star{}, err
	}
	// Whatever happens next, the drive goes back on. A run that dies having left tracking off leaves
	// the telescope sliding away from the target.
	defer func() { _ = mount.SetTracking(context.Background(), true, "sidereal") }()

	drifted, elapsed, err := p.followDrift(ctx, req, first)
	if err != nil {
		return PECCalibration{}, guidestar.Star{}, err
	}

	dx, dy := drifted.X-first.X, drifted.Y-first.Y
	moved := math.Hypot(dx, dy)
	if moved < 5 {
		return PECCalibration{}, guidestar.Star{}, fmt.Errorf(
			"the star only moved %.1f px with the drive off — the mount may not have stopped tracking", moved)
	}

	// The sign here decides everything downstream, so it is worth stating in full.
	//
	// With the drive stopped the axis stands still while local sidereal time runs on, so the right
	// ascension the telescope points at INCREASES at the sidereal rate. The direction the star drifted
	// is therefore the direction of an increasing axis coordinate. Defining the error that way is what
	// makes the correction — the negative of its derivative — subtract when the axis runs ahead. Take
	// the drift as a negative rotation instead and the whole curve inverts: the mount then adds the
	// error rather than removing it, and tracks about twice as badly as with no table at all.
	//
	// The scale needs no cos(dec) anywhere. Pixels measure the SKY, which moves cos(dec) times as far
	// as the axis turns, and this measurement divides a known axis rotation by an observed sky
	// displacement — so the factor is already inside the number.
	cal := PECCalibration{
		AxisArcsecPerPx: device.SiderealArcsecPerSec * elapsed / moved,
		UnitX:           dx / moved,
		UnitY:           dy / moved,
		DriftPx:         moved,
	}
	p.setPhase(PECCalibrating, "one pixel is %.2f″ of axis rotation", cal.AxisArcsecPerPx)
	return cal, first, nil
}

// followDrift tracks the star across the frame while the drive is stopped.
func (p *pecSession) followDrift(ctx context.Context, req PECTrainRequest, from guidestar.Star) (guidestar.Star, float64, error) {
	// Short exposures here: the star is crossing the frame at fifteen arcseconds a second, and a long
	// one would smear it into a streak with no centroid worth measuring.
	fast := req
	fast.ExposureSec = math.Min(req.ExposureSec, 0.5)

	start := nowFunc()
	last := from
	var vx, vy float64
	for {
		if ctx.Err() != nil {
			return guidestar.Star{}, 0, ctx.Err()
		}
		elapsed := nowFunc().Sub(start).Seconds()
		if elapsed >= req.DriftSec {
			return last, elapsed, nil
		}
		// Predict where it has got to, so the search follows the star rather than waiting for it.
		expect := guidestar.Star{X: last.X + vx*fast.ExposureSec, Y: last.Y + vy*fast.ExposureSec}
		next, err := p.grabStar(ctx, fast, &expect)
		if err != nil {
			// Running off the sensor is a normal end to the drift, not a failure — we have what we
			// came for as long as it moved far enough.
			if errors.Is(err, guidestar.ErrNoStar) && math.Hypot(last.X-from.X, last.Y-from.Y) > 20 {
				return last, elapsed, nil
			}
			return guidestar.Star{}, 0, fmt.Errorf("following the star with the drive stopped: %w", err)
		}
		if dt := fast.ExposureSec; dt > 0 {
			vx, vy = (next.X-last.X)/dt, (next.Y-last.Y)/dt
		}
		last = next
	}
}

// driftSearchPx is how far the re-find looks while the drive is off. The star crosses roughly fifteen
// arcseconds a second, which is tens of pixels between frames — far outside the tight search that
// keeps a neighbour from being mistaken for the guide star while tracking.
const driftSearchPx = 60

// grabStar takes one exposure and measures the star. expect is where it should be; nil means pick a
// fresh one.
func (p *pecSession) grabStar(ctx context.Context, req PECTrainRequest, expect *guidestar.Star) (guidestar.Star, error) {
	cam := p.srv.currentCamera()
	if cam == nil {
		return guidestar.Star{}, device.ErrNotConnected
	}
	if err := setExposure(cam, req.ExposureSec, req.Bin); err != nil {
		return guidestar.Star{}, err
	}
	frame, err := p.srv.live.exposeOnce(ctx, cam)
	if err != nil {
		return guidestar.Star{}, err
	}
	im, ok := guidestar.ImageFrom(frame.Pix, frame.Width, frame.Height)
	if !ok {
		return guidestar.Star{}, fmt.Errorf("the frame could not be read")
	}
	if expect == nil {
		return guidestar.Pick(im, guidestar.Options{})
	}
	return guidestar.Refind(im, expect.X, expect.Y, driftSearchPx, guidestar.Options{})
}

// setExposure applies the sampling exposure and binning.
func setExposure(cam device.Camera, seconds float64, bin int) error {
	if err := cam.SetControl(device.ControlExposure, int64(seconds*1e6), false); err != nil {
		return err
	}
	roi := cam.ROI()
	if roi.Bin == bin {
		return nil
	}
	caps := cam.Caps()
	roi.Bin = bin
	roi.X, roi.Y = 0, 0
	roi.Width, roi.Height = caps.MaxWidth/bin, caps.MaxHeight/bin
	_, err := cam.SetROI(roi)
	return err
}
