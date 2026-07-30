package sim

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Camera is a simulated ASI-class mono camera: enumerated controls, a cooler that ramps, still
// exposures that take their exposure time, and a video mode. It renders through the shared World,
// so it sees whatever the simulated mount points at, through whatever filter the simulated wheel
// has in the beam.
type Camera struct {
	mu    sync.Mutex
	world *World

	connected bool
	roi       device.ROI
	controls  map[string]*device.Control
	order     []string

	expStart  time.Time
	expEnd    time.Time
	exposing  bool
	lastFrame *device.Frame
	streaming bool
	dropped   int
	frameSeq  int64
}

// NewCamera builds a simulated camera bound to a world.
func NewCamera(w *World) *Camera {
	c := &Camera{world: w}
	c.reset()
	return c
}

func (c *Camera) reset() {
	cfg := c.world.Config()
	c.roi = device.ROI{Width: cfg.SensorW, Height: cfg.SensorH, Bin: 1, Format: "raw16"}
	c.controls = map[string]*device.Control{}
	c.order = nil
	add := func(ctl device.Control) {
		c.controls[ctl.Name] = &ctl
		c.order = append(c.order, ctl.Name)
	}
	// Ranges mirror an ASI1600MM Pro. Real drivers read these from the SDK; the simulator states
	// them once, here, so nothing downstream is tempted to hardcode them.
	add(device.Control{Name: device.ControlExposure, Label: "Exposure", Min: 32, Max: 2_000_000_000,
		Default: 1_000_000, Value: 1_000_000, Writable: true, Unit: "µs"})
	add(device.Control{Name: device.ControlGain, Label: "Gain", Min: 0, Max: 600,
		Default: 139, Value: 139, Writable: true})
	add(device.Control{Name: device.ControlOffset, Label: "Offset", Min: 0, Max: 255,
		Default: 50, Value: 50, Writable: true})
	add(device.Control{Name: device.ControlTemperature, Label: "Sensor temperature", Min: -500, Max: 1000,
		Value: 200, Unit: "°C", ScaleDivisor: 10})
	add(device.Control{Name: device.ControlTargetTemp, Label: "Target temperature", Min: -40, Max: 30,
		Default: 0, Value: -15, Writable: true, Unit: "°C"})
	add(device.Control{Name: device.ControlCoolerOn, Label: "Cooler", Min: 0, Max: 1,
		Default: 0, Value: 0, Writable: true})
	add(device.Control{Name: device.ControlCoolerPower, Label: "Cooler power", Min: 0, Max: 100,
		Value: 0, Unit: "%"})
	add(device.Control{Name: device.ControlUSBBandwidth, Label: "USB bandwidth", Min: 40, Max: 100,
		Default: 80, Value: 80, Writable: true, Unit: "%"})
	add(device.Control{Name: device.ControlHighSpeed, Label: "High speed mode", Min: 0, Max: 1,
		Default: 0, Value: 0, Writable: true})
}

func (c *Camera) Connect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = true
	return nil
}

func (c *Camera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	c.streaming = false
	c.exposing = false
	return nil
}

func (c *Camera) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Caps describes the simulated sensor.
func (c *Camera) Caps() device.CameraCaps {
	cfg := c.world.Config()
	return device.CameraCaps{
		Info: device.Info{ID: "sim-camera", Name: "Simulated ASI1600MM Pro",
			Driver: "sim", Kind: device.KindCamera},
		MaxWidth: cfg.SensorW, MaxHeight: cfg.SensorH,
		PixelSizeUm: cfg.PixelUm, BitDepth: 12,
		HasCooler: true, HasShutter: false, HasST4: true,
		Bins:       []int{1, 2, 3, 4},
		ImageTypes: []string{"raw16", "raw8"},
		ElecPerADU: 0.3, SerialNumber: "SIM-0001", SDKVersion: "sim",
		MinExposureUs: 32, MaxExposureUs: 2_000_000_000,
	}
}

func (c *Camera) Controls() []device.Control {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshCooler()
	out := make([]device.Control, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, *c.controls[name])
	}
	return out
}

func (c *Camera) Control(name string) (device.Control, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshCooler()
	ctl, ok := c.controls[name]
	if !ok {
		return device.Control{}, false
	}
	return *ctl, true
}

func (c *Camera) SetControl(name string, value int64, auto bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctl, ok := c.controls[name]
	if !ok {
		return fmt.Errorf("%w: control %q", device.ErrUnsupported, name)
	}
	if !ctl.Writable {
		return fmt.Errorf("%w: control %q is read-only", device.ErrUnsupported, name)
	}
	if value < ctl.Min || value > ctl.Max {
		return fmt.Errorf("control %q: %d is outside [%d,%d]", name, value, ctl.Min, ctl.Max)
	}
	ctl.Value = value
	ctl.Auto = auto && ctl.AutoSupport
	if name == device.ControlTargetTemp {
		c.world.mu.Lock()
		c.world.targetTempC = float64(value)
		c.world.mu.Unlock()
	}
	if name == device.ControlCoolerOn {
		c.world.mu.Lock()
		c.world.coolerOn = value != 0
		c.world.mu.Unlock()
	}
	return nil
}

// refreshCooler walks the sensor temperature toward the setpoint. Real cooling is a ramp, and the
// sequencer is expected to wait for it — so the simulator makes waiting mean something.
func (c *Camera) refreshCooler() {
	c.world.mu.Lock()
	defer c.world.mu.Unlock()
	target := 20000.0
	power := int64(0)
	if c.world.coolerOn {
		target = c.world.targetTempC * 1000
		power = 60
	}
	// ~2 °C per second of simulated time: fast enough not to bore a test, slow enough to be a ramp.
	cur := float64(c.world.tempMilliC)
	step := 2000.0
	if math.Abs(target-cur) < step {
		cur = target
	} else if target > cur {
		cur += step
	} else {
		cur -= step
	}
	c.world.tempMilliC = int(cur)
	if ctl, ok := c.controls[device.ControlTemperature]; ok {
		ctl.Value = int64(math.Round(cur / 100)) // reported ×10, like the ZWO SDK
	}
	if ctl, ok := c.controls[device.ControlCoolerPower]; ok {
		ctl.Value = power
	}
}

func (c *Camera) ROI() device.ROI {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roi
}

// SetROI applies the ZWO shape rules (width %8, height %2) and reports what was actually set.
func (c *Camera) SetROI(roi device.ROI) (device.ROI, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg := c.world.Config()
	if roi.Bin <= 0 {
		roi.Bin = 1
	}
	maxW, maxH := cfg.SensorW/roi.Bin, cfg.SensorH/roi.Bin
	if roi.Width <= 0 || roi.Width > maxW {
		roi.Width = maxW
	}
	if roi.Height <= 0 || roi.Height > maxH {
		roi.Height = maxH
	}
	roi.Width -= roi.Width % 8
	roi.Height -= roi.Height % 2
	if roi.Width <= 0 || roi.Height <= 0 {
		return c.roi, fmt.Errorf("roi too small after alignment")
	}
	if roi.X+roi.Width > maxW {
		roi.X = maxW - roi.Width
	}
	if roi.Y+roi.Height > maxH {
		roi.Y = maxH - roi.Height
	}
	if roi.Format == "" {
		roi.Format = "raw16"
	}
	c.roi = roi
	return c.roi, nil
}

func (c *Camera) StartExposure(_ context.Context, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	if c.streaming {
		return fmt.Errorf("%w: video capture is active", device.ErrBusy)
	}
	if c.exposing {
		return fmt.Errorf("%w: an exposure is already running", device.ErrBusy)
	}
	now := c.world.now()
	c.expStart = now
	c.expEnd = now.Add(time.Duration(c.controls[device.ControlExposure].Value) * time.Microsecond)
	c.exposing = true
	c.lastFrame = nil
	return nil
}

func (c *Camera) ExposureState() (device.ExposureState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ExposureIdle, device.ErrNotConnected
	}
	switch {
	case !c.exposing && c.lastFrame != nil:
		return device.ExposureSuccess, nil
	case !c.exposing:
		return device.ExposureIdle, nil
	case c.world.now().Before(c.expEnd):
		return device.ExposureWorking, nil
	}
	c.finishExposureLocked()
	return device.ExposureSuccess, nil
}

func (c *Camera) AbortExposure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exposing = false
	c.lastFrame = nil
	return nil
}

// finishExposureLocked renders the frame the exposure collected.
func (c *Camera) finishExposureLocked() {
	c.exposing = false
	c.lastFrame = c.renderLocked(c.expStart, c.expEnd.Sub(c.expStart))
}

func (c *Camera) Download(context.Context) (*device.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil, device.ErrNotConnected
	}
	if c.exposing {
		if c.world.now().Before(c.expEnd) {
			return nil, fmt.Errorf("%w: exposure still running", device.ErrBusy)
		}
		c.finishExposureLocked()
	}
	if c.lastFrame == nil {
		return nil, fmt.Errorf("no exposure to download")
	}
	frame := c.lastFrame
	c.lastFrame = nil
	return frame, nil
}

func (c *Camera) StartVideo(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	if c.exposing {
		return fmt.Errorf("%w: a still exposure is running", device.ErrBusy)
	}
	c.streaming = true
	return nil
}

func (c *Camera) StopVideo() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streaming = false
	return nil
}

func (c *Camera) Streaming() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streaming
}

func (c *Camera) DroppedFrames() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// NextFrame renders the next video frame, waiting out the exposure time first (capped by timeout,
// like the SDK's own wait).
func (c *Camera) NextFrame(ctx context.Context, timeout time.Duration) (*device.Frame, error) {
	c.mu.Lock()
	if !c.connected {
		c.mu.Unlock()
		return nil, device.ErrNotConnected
	}
	if !c.streaming {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: video capture is not running", device.ErrBusy)
	}
	exposure := time.Duration(c.controls[device.ControlExposure].Value) * time.Microsecond
	c.mu.Unlock()

	wait := exposure
	if timeout > 0 && wait > timeout {
		c.mu.Lock()
		c.dropped++
		c.mu.Unlock()
		return nil, fmt.Errorf("frame timeout after %s", timeout)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(wait):
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.renderLocked(c.world.now().Add(-exposure), exposure), nil
}

// renderLocked paints one frame from the current world state. Caller holds c.mu.
func (c *Camera) renderLocked(start time.Time, dur time.Duration) *device.Frame {
	cfg := c.world.Config()
	c.frameSeq++

	c.world.mu.Lock()
	raDeg, decDeg := c.world.pointingAt(start.Add(dur / 2))
	paDeg := c.world.cameraPADeg
	filter := c.world.filterNameLocked()
	tempMilliC := c.world.tempMilliC
	c.world.mu.Unlock()

	roi := c.roi
	scale := 206.264806 * cfg.PixelUm / cfg.FocalMM * float64(roi.Bin)
	gain := c.controls[device.ControlGain].Value
	offset := c.controls[device.ControlOffset].Value

	pix := renderFrame(renderParams{
		raDeg: raDeg, decDeg: decDeg, paDeg: paDeg,
		width: roi.Width, height: roi.Height, bin: roi.Bin,
		scaleArcsecPx: scale,
		exposureSec:   dur.Seconds(),
		gain:          gain, offset: offset,
		filter:             filter,
		seeingArcsec:       cfg.SeeingArcsec,
		focusOffsetUm:      cfg.FocusOffsetUm,
		fRatio:             cfg.FocalMM / cfg.ApertureMM,
		pixelUm:            cfg.PixelUm,
		skyMagPerAsec:      cfg.SkyMagPerAsec,
		faintPerDeg2:       cfg.FaintStarsPerDeg2,
		flatPanelADUPerSec: cfg.FlatPanelADUPerSec,
		readNoiseADU:       cfg.ReadNoiseADU,
		hotPixels:          cfg.HotPixels,
		epoch:              start,
		seed:               cfg.Seed*1_000_003 + c.frameSeq,
		extra:              cfg.SyntheticStars,
	})
	return &device.Frame{
		Width: roi.Width, Height: roi.Height, Bin: roi.Bin, Pix: pix,
		ExposureUs: dur.Microseconds(), Gain: gain, Offset: offset,
		TempMilliC: tempMilliC, HasTemp: true,
		StartedAt: start, Duration: dur,
	}
}
