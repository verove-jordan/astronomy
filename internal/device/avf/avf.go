// Package avf drives any macOS AVFoundation capture device as a camera — in practice an iPhone,
// through Continuity Camera.
//
// It exists because the phone is the one imaging device already in everybody's pocket, and because
// proving the whole chain — capture, measure a shift, move the mount — needs a camera that can be
// pointed at a bookshelf rather than at the sky. Everything downstream (the live view, the
// sequencer, the guide servo) then works unchanged, because this satisfies device.Camera like the
// ZWO driver does.
//
// # Why ffmpeg rather than AVFoundation directly
//
// AVFoundation is Objective-C, so binding it means cgo, and this engine is deliberately cgo-free
// (see internal/device/nexstar/serial.go for the same decision on the serial side). ffmpeg's
// avfoundation input device exposes the identical camera list and is already a dependency of this
// project. One subprocess, no C.
//
// # Two things learned by pointing it at a real iPhone
//
//   - The first frames are BLACK. Auto-exposure and the sensor's own ramp take about a second, and a
//     capture that grabs frame zero returns an image that looks like a lens cap. The warm-up is paid
//     once, at connect, rather than per exposure.
//   - yuv420p is not among the offered formats; nv12 is. Asking for the wrong one makes ffmpeg print
//     a warning and pick something for you, which is not a thing to leave to chance in a driver.
//
// # What this camera honestly cannot do
//
// A phone auto-exposes, auto-focuses and auto-white-balances, and none of that is controllable
// through avfoundation. So there is no gain, no offset, no cooling, and "exposure" is not an
// integration time — it is how long the driver lets the scene settle before keeping a frame. Those
// controls are reported as read-only rather than pretended to work, because a sequencer that thinks
// it set a 60-second exposure and got 1/30 s would produce data nobody could interpret later.
package avf

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Defaults chosen for guiding rather than for pictures: a small frame correlates fast, and 30 fps is
// what Continuity Camera offers.
const (
	defaultWidth  = 960
	defaultHeight = 720
	defaultFPS    = 30
	// warmupFrames is how many frames are read and thrown away after the stream starts. At 30 fps
	// this is about 1.3 s, measured as comfortably past the point where an iPhone stops returning
	// black.
	warmupFrames = 40
	// frameTimeout bounds a single frame read. Continuity Camera drops out when the phone locks or
	// walks away, and the read must fail rather than hang a capture loop forever.
	frameTimeout = 10 * time.Second
	// connectTimeout covers starting ffmpeg plus the warm-up. Generous, because a phone that has just
	// woken up takes its time, and failing a connect that would have worked is the more annoying error.
	connectTimeout = 20 * time.Second
)

// ErrNoDevice is returned when the named capture device is not present.
var ErrNoDevice = errors.New("no matching AVFoundation capture device")

// Device is one capture device as ffmpeg reports it.
type Device struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

// Camera is an AVFoundation capture device presented as a device.Camera.
type Camera struct {
	mu sync.Mutex

	// name selects the device: an ffmpeg index, or any substring of the device's name. A substring is
	// what a user can actually remember, and indices move when a webcam is plugged in.
	name          string
	width, height int
	fps           int
	binary        string

	cmd    *exec.Cmd
	stdout io.ReadCloser

	// The stream is drained by its own goroutine into a single-frame slot. A consumer that read the
	// pipe directly would get whatever had queued up while it was busy — and on a guide loop that
	// captures every few seconds at 30 fps, that is a correction applied to where the sky was a
	// hundred frames ago. Draining continuously also stops ffmpeg blocking on a full pipe.
	frameMu sync.Mutex
	latest  []byte
	gen     uint64
	// consumed is the generation last handed out, so "give me a frame" can mean a NEW one.
	consumed uint64
	// frameCh is closed and replaced on every frame: a broadcast any number of waiters can select on.
	frameCh chan struct{}
	readErr error

	connected bool
	streaming bool
	dropped   int

	settleUs int64
	// pending holds the frame StartExposure grabbed, so Download can hand it over. The interface
	// splits the two, and the phone gives no way to "start" an exposure and collect it later.
	pending  *device.Frame
	expState device.ExposureState

	// runner and lister are swapped in tests so the whole lifecycle runs with no camera and no ffmpeg.
	runner func(ctx context.Context, args []string) (*exec.Cmd, io.ReadCloser, error)
	lister func(ctx context.Context) ([]Device, error)
}

// Option configures a Camera.
type Option func(*Camera)

// WithSize sets the frame size ffmpeg scales to. Smaller is faster to correlate and quite enough for
// measuring a shift.
func WithSize(w, h int) Option {
	return func(c *Camera) {
		if w > 0 && h > 0 {
			c.width, c.height = w, h
		}
	}
}

// New builds a camera for a device selected by ffmpeg index or by a substring of its name.
func New(name string, opts ...Option) *Camera {
	c := &Camera{
		name:   name,
		width:  defaultWidth,
		height: defaultHeight,
		fps:    defaultFPS,
		binary: "ffmpeg",
	}
	c.runner, c.lister = c.startFFmpeg, c.listDevices
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ device.Camera = (*Camera)(nil)

// deviceLine matches ffmpeg's device listing: "[1] Jordan's iPhone Camera".
var deviceLine = regexp.MustCompile(`^\[(\d+)\]\s+(.+?)\s*$`)

// List reports the machine's video capture devices.
func List(ctx context.Context) ([]Device, error) { return (&Camera{binary: "ffmpeg"}).listDevices(ctx) }

func (c *Camera) listDevices(ctx context.Context) ([]Device, error) {
	// ffmpeg writes the listing to stderr and then exits non-zero because no input was given. That
	// is not a failure, so the exit status is deliberately ignored and only the parse matters.
	cmd := exec.CommandContext(ctx, c.binary, "-hide_banner", "-f", "avfoundation",
		"-list_devices", "true", "-i", "")
	out, _ := cmd.CombinedOutput()
	return parseDevices(string(out)), nil
}

// parseDevices pulls the VIDEO devices out of ffmpeg's listing.
//
// The audio devices are listed in the same format under their own heading, and an iPhone appears in
// both — so a parser that ignored the headings would happily offer you a microphone to take pictures
// with.
func parseDevices(out string) []Device {
	var devs []Device
	video := false
	for _, line := range strings.Split(out, "\n") {
		// Strip ffmpeg's "[AVFoundation indev @ 0x...] " prefix.
		if i := strings.Index(line, "] "); i >= 0 && strings.Contains(line[:i], "AVFoundation") {
			line = line[i+2:]
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "video devices"):
			video = true
			continue
		case strings.Contains(line, "audio devices"):
			video = false
			continue
		}
		if !video {
			continue
		}
		if m := deviceLine.FindStringSubmatch(line); m != nil {
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			devs = append(devs, Device{Index: idx, Name: m[2]})
		}
	}
	return devs
}

// resolve turns the configured selector into an ffmpeg device index.
func (c *Camera) resolve(ctx context.Context) (Device, error) {
	devs, err := c.lister(ctx)
	if err != nil {
		return Device{}, err
	}
	if len(devs) == 0 {
		return Device{}, fmt.Errorf("%w: no video capture devices at all — is the Mac's camera permission granted?", ErrNoDevice)
	}
	// An exact index wins, so a script can pin one.
	if n, err := strconv.Atoi(strings.TrimSpace(c.name)); err == nil {
		for _, d := range devs {
			if d.Index == n {
				return d, nil
			}
		}
		return Device{}, fmt.Errorf("%w: no device with index %d", ErrNoDevice, n)
	}
	// Otherwise a case-insensitive substring, so "iphone" finds "Jordan's iPhone Camera".
	want := strings.ToLower(strings.TrimSpace(c.name))
	if want == "" {
		want = "iphone"
	}
	for _, d := range devs {
		lower := strings.ToLower(d.Name)
		// Continuity Camera also publishes a "Desk View" camera, which points at the desk rather than
		// where the phone is aimed. Picking it by accident would be baffling, so it is excluded unless
		// asked for by name.
		if strings.Contains(lower, "desk view") && !strings.Contains(want, "desk") {
			continue
		}
		if strings.Contains(lower, want) {
			return d, nil
		}
	}
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, fmt.Sprintf("[%d] %s", d.Index, d.Name))
	}
	return Device{}, fmt.Errorf("%w matching %q — saw %s", ErrNoDevice, c.name, strings.Join(names, ", "))
}

// ffmpegArgs builds the capture command: one long-lived process emitting raw 8-bit grayscale.
//
// Grayscale because every consumer here wants luminance — the guide correlator, the focus meter, the
// star detector — and converting in ffmpeg avoids shipping three channels through a pipe to throw
// two away. Scaling to a fixed size makes the frame length constant, which is what lets the reader
// below know where one frame ends and the next begins with no container and no parsing.
func (c *Camera) ffmpegArgs(index int) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-f", "avfoundation",
		"-framerate", strconv.Itoa(c.fps),
		// nv12, because avfoundation does not offer yuv420p and letting ffmpeg guess is not a thing to
		// leave to chance in a driver.
		"-pixel_format", "nv12",
		"-i", strconv.Itoa(index),
		"-vf", fmt.Sprintf("scale=%d:%d", c.width, c.height),
		"-f", "rawvideo", "-pix_fmt", "gray", "-",
	}
}

func (c *Camera) startFFmpeg(ctx context.Context, args []string) (*exec.Cmd, io.ReadCloser, error) {
	cmd := exec.Command(c.binary, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start %s: %w", c.binary, err)
	}
	return cmd, out, nil
}

// Connect opens the stream and pays the warm-up once.
func (c *Camera) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connected {
		return nil
	}
	dev, err := c.resolve(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", device.ErrDriverUnavailable, err)
	}
	cmd, stdout, err := c.runner(ctx, c.ffmpegArgs(dev.Index))
	if err != nil {
		return fmt.Errorf("%w: %v", device.ErrDriverUnavailable, err)
	}
	c.cmd, c.stdout = cmd, stdout
	c.name = dev.Name
	c.connected = true
	c.frameMu.Lock()
	c.latest, c.gen, c.consumed, c.readErr = nil, 0, 0, nil
	c.frameCh = make(chan struct{})
	c.frameMu.Unlock()

	go c.readLoop(bufio.NewReaderSize(stdout, c.frameLen()*2), warmupFrames)

	// The warm-up is paid once, here, rather than per exposure: an iPhone returns black for about a
	// second while auto-exposure engages, and a capture loop paying that on every frame would spend
	// its night waiting instead of guiding. Waiting for the first GOOD frame also means a stream that
	// dies immediately fails Connect rather than reporting a camera that then hands out nothing.
	c.mu.Unlock()
	_, _, err = c.waitFrame(ctx, 0, connectTimeout)
	c.mu.Lock()
	if err != nil {
		c.closeLocked()
		return fmt.Errorf("%w: no usable frame after warm-up (%v)", device.ErrDriverUnavailable, err)
	}
	return nil
}

// readLoop drains the stream forever, keeping only the newest frame.
func (c *Camera) readLoop(r *bufio.Reader, warm int) {
	for i := 0; ; i++ {
		buf := make([]byte, c.frameLen())
		// A raw stream has no framing, so a short read is not "a small frame" — it is the point at
		// which every later frame is offset by the shortfall and the picture shears diagonally
		// forever. ReadFull is what makes that impossible.
		if _, err := io.ReadFull(r, buf); err != nil {
			c.frameMu.Lock()
			c.readErr = err
			ch := c.frameCh
			c.frameCh = make(chan struct{})
			c.frameMu.Unlock()
			close(ch)
			return
		}
		if i < warm {
			continue
		}
		c.publish(buf)
	}
}

func (c *Camera) publish(buf []byte) {
	c.frameMu.Lock()
	if c.gen > c.consumed {
		// The previous frame was never taken. Counting that is what turns "the preview looks jerky"
		// into a number somebody can act on.
		c.dropped++
	}
	c.latest, c.gen = buf, c.gen+1
	ch := c.frameCh
	c.frameCh = make(chan struct{})
	c.frameMu.Unlock()
	close(ch)
}

// waitFrame returns the newest frame whose generation is greater than after.
func (c *Camera) waitFrame(ctx context.Context, after uint64, timeout time.Duration) ([]byte, uint64, error) {
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for {
		c.frameMu.Lock()
		if c.gen > after {
			buf, gen := c.latest, c.gen
			c.consumed = gen
			c.frameMu.Unlock()
			return buf, gen, nil
		}
		if c.readErr != nil {
			err := c.readErr
			c.frameMu.Unlock()
			return nil, 0, err
		}
		ch := c.frameCh
		c.frameMu.Unlock()

		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-deadline:
			// Continuity Camera stops delivering when the phone locks or wanders out of range. Saying
			// so beats hanging a capture loop for the rest of the night.
			return nil, 0, fmt.Errorf("no frame within %s — has the phone locked or moved out of range?", timeout)
		case <-ch:
		}
	}
}

// lastGen reports the generation already handed out, so a caller can ask for a newer one.
func (c *Camera) lastGen() uint64 {
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	return c.consumed
}

func (c *Camera) frameLen() int { return c.width * c.height }

func (c *Camera) closeLocked() {
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.cmd, c.stdout = nil, nil
	c.connected, c.streaming = false, false
}

func (c *Camera) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	return nil
}

func (c *Camera) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

func (c *Camera) Caps() device.CameraCaps {
	c.mu.Lock()
	defer c.mu.Unlock()
	return device.CameraCaps{
		Info:      device.Info{ID: "avf", Name: c.name, Driver: "avf", Kind: device.KindCamera},
		MaxWidth:  c.width,
		MaxHeight: c.height,
		// A phone sensor's real pixel pitch is around a micron, but the frame has been scaled by
		// ffmpeg, so any number here would be fiction. Zero says "unknown" rather than inventing an
		// image scale that a plate-solve would then trust.
		PixelSizeUm: 0,
		BitDepth:    8,
		IsColor:     false,
		Bins:        []int{1},
		ImageTypes:  []string{"y8"},
	}
}

// Controls reports what can be set — which is almost nothing, honestly rather than hopefully.
func (c *Camera) Controls() []device.Control {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []device.Control{{
		Name: "exposure", Label: "Settle time", Min: 0, Max: 10_000_000,
		Default: 0, Value: c.settleUs, Writable: true, Unit: "µs",
		Description: "How long to let the scene settle before keeping a frame. This camera auto-exposes: it is NOT an integration time.",
	}, {
		Name: "gain", Label: "Gain", Min: 0, Max: 0, Value: 0, Writable: false,
		Description: "The phone chooses its own gain; AVFoundation exposes no way to set it.",
	}}
}

func (c *Camera) Control(name string) (device.Control, bool) {
	for _, ctrl := range c.Controls() {
		if ctrl.Name == name {
			return ctrl, true
		}
	}
	return device.Control{}, false
}

func (c *Camera) SetControl(name string, value int64, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name != "exposure" {
		// Refused rather than silently accepted: a caller that believes it set the gain would record a
		// gain in the FITS header that the sensor never used.
		return fmt.Errorf("%w: %q is not settable on an AVFoundation camera", device.ErrUnsupported, name)
	}
	if value < 0 {
		value = 0
	}
	c.settleUs = value
	return nil
}

func (c *Camera) ROI() device.ROI {
	c.mu.Lock()
	defer c.mu.Unlock()
	return device.ROI{Width: c.width, Height: c.height, Bin: 1, Format: "y8"}
}

// SetROI accepts only the full frame. ffmpeg could crop, but a driver that silently returned
// something other than what was asked for is how a guide star ends up measured against the wrong
// origin.
func (c *Camera) SetROI(roi device.ROI) (device.ROI, error) {
	current := c.ROI()
	if roi.Width != 0 && (roi.Width != current.Width || roi.Height != current.Height) {
		return current, fmt.Errorf("%w: this camera captures the full frame only (%dx%d)",
			device.ErrUnsupported, current.Width, current.Height)
	}
	return current, nil
}

// StartExposure keeps the next frame off the stream, after any configured settle.
func (c *Camera) StartExposure(ctx context.Context, _ bool) error {
	c.mu.Lock()
	settle := time.Duration(c.settleUs) * time.Microsecond
	c.expState = device.ExposureWorking
	c.mu.Unlock()

	if settle > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settle):
		}
	}

	if !c.Connected() {
		c.setExposureState(device.ExposureFailed)
		return device.ErrNotConnected
	}
	started := time.Now()
	// A NEW frame, not whatever is already in hand: asking for "the latest" would let two exposures
	// in quick succession return the same picture, and a guide loop would then measure a drift of
	// exactly zero and correct nothing.
	buf, _, err := c.waitFrame(ctx, c.lastGen(), frameTimeout)
	if err != nil {
		c.setExposureState(device.ExposureFailed)
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = &device.Frame{
		Width: c.width, Height: c.height, Bin: 1,
		Pix:       expandTo16(buf),
		StartedAt: started, Duration: time.Since(started),
	}
	c.expState = device.ExposureSuccess
	return nil
}

func (c *Camera) setExposureState(s device.ExposureState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expState = s
}

func (c *Camera) ExposureState() (device.ExposureState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expState == "" {
		return device.ExposureIdle, nil
	}
	return c.expState, nil
}

func (c *Camera) AbortExposure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending, c.expState = nil, device.ExposureIdle
	return nil
}

func (c *Camera) Download(ctx context.Context) (*device.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return nil, fmt.Errorf("%w: no exposure has completed", device.ErrBusy)
	}
	frame := c.pending
	c.pending, c.expState = nil, device.ExposureIdle
	return frame, nil
}

// StartVideo is a no-op beyond a flag: the stream is always running, which is exactly what makes
// this camera good at previewing.
func (c *Camera) StartVideo(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected {
		return device.ErrNotConnected
	}
	c.streaming = true
	return nil
}

func (c *Camera) NextFrame(ctx context.Context, timeout time.Duration) (*device.Frame, error) {
	if timeout <= 0 {
		timeout = frameTimeout
	}
	buf, _, err := c.waitFrame(ctx, c.lastGen(), timeout)
	if err != nil {
		return nil, err
	}
	return &device.Frame{
		Width: c.width, Height: c.height, Bin: 1,
		Pix: expandTo16(buf), StartedAt: time.Now(),
	}, nil
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
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	return c.dropped
}

// expandTo16 lifts 8-bit samples into the 16-bit frame buffer the rest of the app works in.
//
// Replicating the byte into both halves (v<<8 | v) rather than shifting it keeps full scale at
// 0xFFFF: a plain v<<8 would cap a white pixel at 0xFF00, and every downstream saturation test —
// flat clipping, star-core rejection — would then never fire.
func expandTo16(b []byte) []uint16 {
	out := make([]uint16, len(b))
	for i, v := range b {
		out[i] = uint16(v)<<8 | uint16(v)
	}
	return out
}

// Probe reports whether this machine has anything worth offering as a camera.
func Probe() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg is not installed — it is how this driver reaches AVFoundation without cgo")
	}
	devs, err := List(ctx)
	if err != nil || len(devs) == 0 {
		return "", fmt.Errorf("no AVFoundation video devices (check Camera permission in System Settings → Privacy & Security)")
	}
	names := make([]string, 0, len(devs))
	for _, d := range devs {
		names = append(names, fmt.Sprintf("[%d] %s", d.Index, d.Name))
	}
	return "capture devices: " + strings.Join(names, ", "), nil
}
