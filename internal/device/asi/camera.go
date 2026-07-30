package asi

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The ASI camera driver.
//
// Two rules shape this file, both learned from how the SDK actually behaves rather than from its
// documentation:
//
//  1. Every SDK call is serialised behind one mutex. ASICamera2 is not documented as thread-safe,
//     and the live view, the sequencer and the status poller all touch it concurrently.
//  2. Long exposures are POLLED (ASIGetExpStatus), never waited on inside ASIGetDataAfterExp.
//     Blocking in the SDK for five minutes would make a cancel impossible and hold the mutex for
//     the whole exposure, freezing status and the abort button with it.

// Camera implements device.Camera over the ZWO SDK.
type Camera struct {
	mu   sync.Mutex
	sdk  *sdk
	id   int32
	info CameraInfo

	connected bool
	streaming bool
	// expStartedAt is when the current still exposure actually began, recorded at StartExposure so
	// readout and download latency cannot creep into it. Zero for video frames, which have no such
	// moment and fall back to reconstruction.
	expStartedAt time.Time
	caps         device.CameraCaps
	controls     []device.Control
	// ctrlMap is built at connect from what the camera reports, so control ids never come from a
	// hardcoded table. See controls.go for why that matters.
	ctrlMap *controlMap
	// injected replaces the vendor library in tests; nil in every real build.
	injected *sdk
	roi      device.ROI

	exposureUs int64
	gain       int64
	offset     int64
	// buf is reused between frames: a full-frame 16-bit ASI1600 download is 33 MB, and allocating
	// that per frame at planetary frame rates would keep the collector busy for no reason.
	buf []byte
}

// New builds an unconnected camera bound to the first ZWO camera found.
func New() *Camera { return &Camera{} }

// resolveSDK returns the vendor library, or an injected stand-in. The seam exists so the driver's
// whole lifecycle — controls, ROI rules, exposure states, download, video — can be tested without a
// camera or a ZWO library present, which is otherwise impossible on a machine with neither.
func (c *Camera) resolveSDK() (*sdk, error) {
	if c.injected != nil {
		return c.injected, nil
	}
	return load()
}

// Connect opens the camera and reads everything the UI needs from the device itself.
func (c *Camera) Connect(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		return nil
	}
	s, err := c.resolveSDK()
	if err != nil {
		return err
	}
	if n := s.getNumOfConnectedCameras(); n <= 0 {
		return fmt.Errorf("asi: no ZWO camera is connected")
	}
	buf := make([]byte, infoStructSize)
	if err := check(s.getCameraProperty(&buf[0], 0)); err != nil {
		return err
	}
	info := parseCameraInfo(buf)

	if err := check(s.openCamera(info.ID)); err != nil {
		return err
	}
	if err := check(s.initCamera(info.ID)); err != nil {
		_ = s.closeCamera(info.ID)
		return err
	}

	c.sdk, c.id, c.info, c.connected = s, info.ID, info, true
	c.controls = c.readControlsLocked()
	// Say so now if the camera never reported something the sequencer relies on. Discovering it
	// mid-run looks like a capture bug rather than a driver/SDK mismatch.
	log.Printf("asi: %s controls resolved: %s", info.Name, c.ctrlMap.describe())
	if missing := c.ctrlMap.missingEssentials(); len(missing) > 0 {
		log.Printf("asi: %s did not report these controls: %s — captures using them will fail",
			info.Name, strings.Join(missing, ", "))
	}
	c.caps = c.buildCapsLocked()
	// Full frame, 16-bit, unbinned. RAW16 rather than RAW8 always: the whole point of this camera is
	// its 12-bit ADC, and RAW8 would throw four bits away before anything could use them.
	c.roi = device.ROI{Width: int(info.MaxWidth), Height: int(info.MaxHeight), Bin: 1}
	if err := check(s.setROIFormat(c.id, int32(info.MaxWidth), int32(info.MaxHeight), 1, imgRaw16)); err != nil {
		c.closeLocked()
		return fmt.Errorf("asi: could not set the full-frame 16-bit format: %w", err)
	}
	return nil
}

// readControlsLocked asks the camera what it can do. Every range in the UI comes from here — never
// from a hardcoded table, because the numbers differ between models and firmware.
func (c *Camera) readControlsLocked() []device.Control {
	c.ctrlMap = newControlMap()
	var n int32
	if err := check(c.sdk.getNumOfControls(c.id, &n)); err != nil || n <= 0 {
		return nil
	}
	buf := make([]byte, capsStructSize)
	out := make([]device.Control, 0, n)
	for i := int32(0); i < n; i++ {
		if err := check(c.sdk.getControlCaps(c.id, i, &buf[0])); err != nil {
			continue
		}
		caps := parseControlCaps(buf)
		// The id comes from the camera's own report, never from a guess about the enum.
		name := canonicalControlName(caps.Name)
		if name == "" {
			continue
		}
		c.ctrlMap.put(name, caps.ControlType)

		var value int64
		var auto int32
		_ = check(c.sdk.getControlValue(c.id, caps.ControlType, &value, &auto))
		out = append(out, device.Control{
			Name: name, Label: caps.Name, Description: caps.Description,
			Min: caps.Min, Max: caps.Max, Default: caps.Default, Value: value,
			Writable: caps.Writable, AutoSupport: caps.AutoSupport, Auto: auto != 0,
			Unit: controlUnit(name), ScaleDivisor: scaleDivisor(name),
		})
	}
	return out
}

// buildCapsLocked describes the camera to the UI.
func (c *Camera) buildCapsLocked() device.CameraCaps {
	caps := device.CameraCaps{
		Info: device.Info{
			ID:     fmt.Sprintf("asi-%d", c.info.ID),
			Name:   c.info.Name,
			Driver: "asi",
			Kind:   "camera",
		},
		MaxWidth:     int(c.info.MaxWidth),
		MaxHeight:    int(c.info.MaxHeight),
		PixelSizeUm:  c.info.PixelSizeUm,
		IsColor:      c.info.IsColor,
		BayerPattern: bayerName(c.info.BayerPattern),
		Bins:         c.info.Bins,
		BitDepth:     16,
		ImageTypes:   []string{"raw16", "raw8"},
		HasShutter:   false, // ZWO CMOS cameras are shutterless; a "dark" simply means capped
	}
	for _, ctrl := range c.controls {
		switch ctrl.Name {
		case device.ControlExposure:
			caps.MinExposureUs, caps.MaxExposureUs = ctrl.Min, ctrl.Max
		case device.ControlCoolerOn:
			caps.HasCooler = true
		}
	}
	return caps
}

// bayerName maps the SDK's pattern enum. Order is RG, BG, GR, GB in ASICamera2.h.
func bayerName(p int32) string {
	switch p {
	case 0:
		return "RGGB"
	case 1:
		return "BGGR"
	case 2:
		return "GRBG"
	case 3:
		return "GBRG"
	default:
		return ""
	}
}

func (c *Camera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeLocked()
}

func (c *Camera) closeLocked() error {
	if !c.connected {
		return nil
	}
	if c.streaming {
		_ = c.sdk.stopVideoCapture(c.id)
		c.streaming = false
	}
	err := check(c.sdk.closeCamera(c.id))
	c.connected = false
	c.buf = nil
	return err
}

func (c *Camera) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Camera) Caps() device.CameraCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps
}

// Controls re-reads the live values, so temperature and cooler power are current.
func (c *Camera) Controls() []device.Control {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil
	}
	for i := range c.controls {
		t, ok := c.ctrlMap.id(c.controls[i].Name)
		if !ok {
			continue
		}
		var value int64
		var auto int32
		if err := check(c.sdk.getControlValue(c.id, t, &value, &auto)); err == nil {
			c.controls[i].Value, c.controls[i].Auto = value, auto != 0
		}
	}
	return append([]device.Control(nil), c.controls...)
}

func (c *Camera) Control(name string) (device.Control, bool) {
	for _, ctrl := range c.Controls() {
		if ctrl.Name == name {
			return ctrl, true
		}
	}
	return device.Control{}, false
}

// SetControl writes a control, clamped to what the camera says it accepts. Clamping rather than
// erroring because a UI slider at its stop should mean "as far as it goes", not a failed capture.
func (c *Camera) SetControl(name string, value int64, auto bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	t, ok := c.ctrlMap.id(name)
	if !ok {
		return fmt.Errorf("asi: this camera does not have a %q control", name)
	}
	for _, ctrl := range c.controls {
		if ctrl.Name != name {
			continue
		}
		if !ctrl.Writable {
			return fmt.Errorf("asi: %s is read-only on this camera", name)
		}
		if value < ctrl.Min {
			value = ctrl.Min
		}
		if value > ctrl.Max {
			value = ctrl.Max
		}
	}
	autoFlag := int32(0)
	if auto {
		autoFlag = 1
	}
	if err := check(c.sdk.setControlValue(c.id, t, value, autoFlag)); err != nil {
		return err
	}
	switch name {
	case device.ControlExposure:
		c.exposureUs = value
	case device.ControlGain:
		c.gain = value
	case device.ControlOffset:
		c.offset = value
	}
	return nil
}

func (c *Camera) ROI() device.ROI {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.roi
}

// SetROI changes the read-out window. The SDK requires the width to be a multiple of 8 and the
// height a multiple of 2; violating that returns an error rather than silently reading a shifted
// frame, so the request is rounded here.
func (c *Camera) SetROI(roi device.ROI) (device.ROI, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ROI{}, device.ErrNotConnected
	}
	if c.streaming {
		return c.roi, device.ErrBusy
	}
	bin := roi.Bin
	if bin <= 0 {
		bin = 1
	}
	w, h := roi.Width, roi.Height
	if w <= 0 || h <= 0 {
		w, h = int(c.info.MaxWidth)/bin, int(c.info.MaxHeight)/bin
	}
	w -= w % 8
	h -= h % 2
	if w <= 0 || h <= 0 {
		return c.roi, fmt.Errorf("asi: the requested ROI is too small")
	}
	if err := check(c.sdk.setROIFormat(c.id, int32(w), int32(h), int32(bin), imgRaw16)); err != nil {
		return c.roi, err
	}
	if roi.X > 0 || roi.Y > 0 {
		if err := check(c.sdk.setStartPos(c.id, int32(roi.X), int32(roi.Y))); err != nil {
			return c.roi, err
		}
	}
	c.roi = device.ROI{X: roi.X, Y: roi.Y, Width: w, Height: h, Bin: bin}
	c.buf = nil // the frame size changed
	return c.roi, nil
}

func (c *Camera) StartExposure(_ context.Context, dark bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	if c.streaming {
		return device.ErrBusy
	}
	flag := int32(0)
	if dark {
		flag = 1
	}
	if err := check(c.sdk.startExposure(c.id, flag)); err != nil {
		return err
	}
	// Stamp the start HERE, not at download.
	//
	// Reconstructing it later as "now minus the exposure" quietly folds in the readout and download
	// time, plus however long the caller took to notice the exposure had finished — a few hundred
	// milliseconds, all of it late. Nobody notices on a five-minute sub. It matters for anything that
	// asks WHEN a frame was taken to a fraction of a second, such as folding frames onto the RA
	// worm's rotation, where a third of a second is several per cent of a bin.
	c.expStartedAt = time.Now()
	return nil
}

func (c *Camera) ExposureState() (device.ExposureState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ExposureIdle, device.ErrNotConnected
	}
	var status int32
	if err := check(c.sdk.getExpStatus(c.id, &status)); err != nil {
		return device.ExposureIdle, err
	}
	switch status {
	case expWorking:
		return device.ExposureWorking, nil
	case expSuccess:
		return device.ExposureSuccess, nil
	case expFailed:
		return device.ExposureFailed, nil
	default:
		return device.ExposureIdle, nil
	}
}

func (c *Camera) AbortExposure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	return check(c.sdk.stopExposure(c.id))
}

// Download fetches a finished exposure.
func (c *Camera) Download(context.Context) (*device.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil, device.ErrNotConnected
	}
	buf := c.frameBufferLocked()
	// The SDK writes into this buffer from C. Pinning the goroutine for the call keeps the Go
	// runtime from moving the goroutine's stack under it on a preemption.
	runtime.LockOSThread()
	err := check(c.sdk.getDataAfterExp(c.id, &buf[0], int64(len(buf))))
	runtime.UnlockOSThread()
	if err != nil {
		return nil, err
	}
	return c.frameFromBufferLocked(buf, c.expStartedAt), nil
}

// frameBufferLocked returns the reusable download buffer, sized for the current ROI.
func (c *Camera) frameBufferLocked() []byte {
	need := c.roi.Width * c.roi.Height * 2 // RAW16
	if len(c.buf) != need {
		c.buf = make([]byte, need)
	}
	return c.buf
}

// frameFromBufferLocked reinterprets the download buffer as 16-bit samples and copies them out.
// The copy is deliberate: the buffer is reused for the next frame, so handing out a view over it
// would let a caller's frame change under them mid-stack.
//
// startedAt is passed in because the two callers know different things: a still exposure recorded the
// real moment the shutter opened, while a video frame arrives off a free-running stream with no such
// moment to have recorded, and has to fall back to reconstruction.
func (c *Camera) frameFromBufferLocked(buf []byte, startedAt time.Time) *device.Frame {
	n := len(buf) / 2
	pix := make([]uint16, n)
	src := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), n)
	copy(pix, src)

	if startedAt.IsZero() {
		startedAt = time.Now().Add(-time.Duration(c.exposureUs) * time.Microsecond)
	}
	f := &device.Frame{
		Width: c.roi.Width, Height: c.roi.Height, Bin: c.roi.Bin, Pix: pix,
		ExposureUs: c.exposureUs, Gain: c.gain, Offset: c.offset,
		StartedAt: startedAt,
		Duration:  time.Duration(c.exposureUs) * time.Microsecond,
	}
	if t, ok := c.ctrlMap.id(device.ControlTemperature); ok {
		var temp int64
		var auto int32
		if err := check(c.sdk.getControlValue(c.id, t, &temp, &auto)); err == nil {
			// The SDK reports temperature in TENTHS of a degree; the engine stores milli-°C.
			f.TempMilliC, f.HasTemp = int(temp)*100, true
		}
	}
	return f
}

func (c *Camera) StartVideo(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	if c.streaming {
		return nil
	}
	if err := check(c.sdk.startVideoCapture(c.id)); err != nil {
		return err
	}
	c.streaming = true
	return nil
}

// NextFrame pulls the next streamed frame. The SDK's own wait is used here — unlike a still
// exposure it is bounded by the frame interval, so it cannot hold the lock for minutes.
func (c *Camera) NextFrame(_ context.Context, timeout time.Duration) (*device.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return nil, device.ErrNotConnected
	}
	if !c.streaming {
		return nil, device.ErrBusy
	}
	buf := c.frameBufferLocked()
	waitMs := int32(timeout / time.Millisecond)
	if waitMs <= 0 {
		waitMs = 1000
	}
	runtime.LockOSThread()
	err := check(c.sdk.getVideoData(c.id, &buf[0], int64(len(buf)), waitMs))
	runtime.UnlockOSThread()
	if err != nil {
		return nil, err
	}
	return c.frameFromBufferLocked(buf, time.Time{}), nil
}

func (c *Camera) StopVideo() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected || !c.streaming {
		return nil
	}
	err := check(c.sdk.stopVideoCapture(c.id))
	c.streaming = false
	return err
}

func (c *Camera) Streaming() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streaming
}

func (c *Camera) DroppedFrames() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return 0
	}
	var n int32
	if err := check(c.sdk.getDroppedFrames(c.id, &n)); err != nil {
		return 0
	}
	return int(n)
}
