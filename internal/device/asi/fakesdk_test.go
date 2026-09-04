package asi

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// A pure-Go stand-in for ZWO's library.
//
// The driver's `sdk` is a struct of function fields, so a fake needs no C, no dylib and no camera —
// which matters, because on this machine there is neither ZWO hardware nor an arm64 library. What
// this cannot test is the C ABI itself (that is what the byte-offset tests and the Rosetta smoke
// test cover); what it CAN test is everything above it: the connect lifecycle, control dispatch
// through the runtime map, clamping, the ROI rounding rules, exposure state handling, buffer reuse
// and video mode. All of that is real logic that would otherwise first run at the telescope.

// fakeControl is one control the fake camera reports, as ASIGetControlCaps would.
type fakeControl struct {
	name     string
	id       int32
	min, max int64
	def      int64
	writable bool
	auto     bool
}

// asi1600Controls mirrors what a real ASI1600MM Pro reports — including the ids that make the
// hardcoded-enum approach dangerous: Temperature is 8 and CoolerOn is 17, NOT the other way round.
func asi1600Controls() []fakeControl {
	return []fakeControl{
		{name: "Gain", id: 0, min: 0, max: 600, def: 0, writable: true, auto: true},
		{name: "Exposure", id: 1, min: 32, max: 2_000_000_000, def: 10000, writable: true, auto: true},
		{name: "Offset", id: 5, min: 0, max: 255, def: 10, writable: true},
		{name: "BandWidth", id: 6, min: 40, max: 100, def: 50, writable: true, auto: true},
		{name: "Temperature", id: 8, min: -500, max: 1000, def: 200}, // read-only, tenths °C
		{name: "HighSpeedMode", id: 14, min: 0, max: 1, def: 0, writable: true},
		{name: "CoolerPowerPerc", id: 15, min: 0, max: 100, def: 0}, // read-only
		{name: "TargetTemp", id: 16, min: -40, max: 30, def: 0, writable: true},
		{name: "CoolerOn", id: 17, min: 0, max: 1, def: 0, writable: true},
		{name: "MonoBin", id: 18, min: 0, max: 1, def: 0, writable: true},
		{name: "Fan", id: 19, min: 0, max: 1, def: 1, writable: true},
		{name: "AntiDewHeater", id: 21, min: 0, max: 1, def: 0, writable: true},
	}
}

// fakeCamera is the simulated device behind the fake SDK.
type fakeCamera struct {
	mu sync.Mutex

	cameras   int32
	name      string
	maxW      int64
	maxH      int64
	pixelUm   float64
	controls  []fakeControl
	values    map[int32]int64
	autos     map[int32]int32
	roiW      int32
	roiH      int32
	roiBin    int32
	imgType   int32
	opened    bool
	streaming bool
	expState  int32
	// setCalls records every control write, so a test can prove WHICH SDK id was driven.
	setCalls []struct {
		id    int32
		value int64
	}
	failROI bool
}

func newFakeCamera() *fakeCamera {
	f := &fakeCamera{
		cameras: 1, name: "ZWO ASI1600MM Pro", maxW: 4656, maxH: 3520, pixelUm: 3.8,
		controls: asi1600Controls(),
		values:   map[int32]int64{}, autos: map[int32]int32{},
	}
	for _, c := range f.controls {
		f.values[c.id] = c.def
	}
	return f
}

// sdk builds the function table the driver uses.
func (f *fakeCamera) sdk() *sdk {
	return &sdk{
		getNumOfConnectedCameras: func() int32 { return f.cameras },
		getCameraProperty: func(info *byte, index int32) int32 {
			if index != 0 || f.cameras == 0 {
				return int32(asiErrorInvalidIndex)
			}
			b := unsafe.Slice(info, infoStructSize)
			for i := range b {
				b[i] = 0
			}
			copy(b[offName:], f.name)
			binary.LittleEndian.PutUint32(b[offCameraID:], 0)
			binary.LittleEndian.PutUint64(b[offMaxHeight:], uint64(f.maxH))
			binary.LittleEndian.PutUint64(b[offMaxWidth:], uint64(f.maxW))
			binary.LittleEndian.PutUint32(b[offIsColorCam:], 0)
			binary.LittleEndian.PutUint32(b[offSupportedBins:], 1)
			binary.LittleEndian.PutUint32(b[offSupportedBins+4:], 2)
			binary.LittleEndian.PutUint64(b[offPixelSize:], math.Float64bits(f.pixelUm))
			return 0
		},
		openCamera:  func(int32) int32 { f.opened = true; return 0 },
		initCamera:  func(int32) int32 { return 0 },
		closeCamera: func(int32) int32 { f.opened = false; return 0 },
		getNumOfControls: func(_ int32, n *int32) int32 {
			*n = int32(len(f.controls))
			return 0
		},
		getControlCaps: func(_ int32, index int32, caps *byte) int32 {
			if int(index) >= len(f.controls) {
				return int32(asiErrorInvalidIndex)
			}
			c := f.controls[index]
			b := unsafe.Slice(caps, capsStructSize)
			for i := range b {
				b[i] = 0
			}
			copy(b[offCapsName:], c.name)
			copy(b[offCapsDescription:], c.name+" description")
			binary.LittleEndian.PutUint64(b[offCapsMax:], uint64(c.max))
			binary.LittleEndian.PutUint64(b[offCapsMin:], uint64(c.min))
			binary.LittleEndian.PutUint64(b[offCapsDefault:], uint64(c.def))
			if c.auto {
				binary.LittleEndian.PutUint32(b[offCapsAutoSupport:], 1)
			}
			if c.writable {
				binary.LittleEndian.PutUint32(b[offCapsWritable:], 1)
			}
			binary.LittleEndian.PutUint32(b[offCapsControlType:], uint32(c.id))
			return 0
		},
		getControlValue: func(_ int32, ctrl int32, value *int64, auto *int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			v, ok := f.values[ctrl]
			if !ok {
				return int32(asiErrorInvalidID)
			}
			*value, *auto = v, f.autos[ctrl]
			return 0
		},
		setControlValue: func(_ int32, ctrl int32, value int64, auto int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, ok := f.values[ctrl]; !ok {
				return int32(asiErrorInvalidID)
			}
			f.values[ctrl], f.autos[ctrl] = value, auto
			f.setCalls = append(f.setCalls, struct {
				id    int32
				value int64
			}{ctrl, value})
			return 0
		},
		setROIFormat: func(_ int32, w, h, bin, imgType int32) int32 {
			if f.failROI {
				return int32(asiErrorInvalidID)
			}
			// The real SDK enforces these; the fake does too, so the driver's rounding is tested
			// against the same rule rather than against a permissive stub.
			if w%8 != 0 || h%2 != 0 || w <= 0 || h <= 0 {
				return int32(asiErrorInvalidID)
			}
			f.roiW, f.roiH, f.roiBin, f.imgType = w, h, bin, imgType
			return 0
		},
		getROIFormat: func(_ int32, w, h, bin, imgType *int32) int32 {
			*w, *h, *bin, *imgType = f.roiW, f.roiH, f.roiBin, f.imgType
			return 0
		},
		setStartPos: func(int32, int32, int32) int32 { return 0 },
		getStartPos: func(_ int32, x, y *int32) int32 { *x, *y = 0, 0; return 0 },
		startExposure: func(int32, int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.streaming {
				return int32(asiErrorVideoModeActive)
			}
			f.expState = expWorking
			return 0
		},
		stopExposure: func(int32) int32 { f.mu.Lock(); f.expState = expIdle; f.mu.Unlock(); return 0 },
		getExpStatus: func(_ int32, status *int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			*status = f.expState
			return 0
		},
		getDataAfterExp: func(_ int32, buf *byte, size int64) int32 {
			want := int64(f.roiW) * int64(f.roiH) * 2
			if size < want {
				return int32(asiErrorBufferTooSmall)
			}
			b := unsafe.Slice(buf, size)
			// A recognisable ramp, so a test can prove the pixels really came from here.
			for i := int64(0); i+1 < size; i += 2 {
				binary.LittleEndian.PutUint16(b[i:], uint16(i/2))
			}
			return 0
		},
		startVideoCapture: func(int32) int32 { f.mu.Lock(); f.streaming = true; f.mu.Unlock(); return 0 },
		stopVideoCapture:  func(int32) int32 { f.mu.Lock(); f.streaming = false; f.mu.Unlock(); return 0 },
		getVideoData: func(_ int32, buf *byte, size int64, _ int32) int32 {
			f.mu.Lock()
			streaming := f.streaming
			f.mu.Unlock()
			if !streaming {
				return int32(asiErrorTimeout)
			}
			b := unsafe.Slice(buf, size)
			for i := int64(0); i+1 < size; i += 2 {
				binary.LittleEndian.PutUint16(b[i:], 7)
			}
			return 0
		},
		getSDKVersion:    func() uintptr { return 0 },
		getDroppedFrames: func(_ int32, dropped *int32) int32 { *dropped = 3; return 0 },
	}
}

// finishExposure marks the pending exposure complete, as the sensor would.
func (f *fakeCamera) finishExposure() {
	f.mu.Lock()
	f.expState = expSuccess
	f.mu.Unlock()
}

// fakeRig connects a Camera to a fake device, with a small sensor so downloads stay cheap.
func fakeRig(t *testing.T) (*Camera, *fakeCamera) {
	t.Helper()
	fake := newFakeCamera()
	fake.maxW, fake.maxH = 64, 48
	cam := &Camera{injected: fake.sdk()}
	require.NoError(t, cam.Connect(context.Background()))
	t.Cleanup(func() { _ = cam.Close() })
	return cam, fake
}

func TestCamera_ConnectReadsEverythingFromTheDevice(t *testing.T) {
	fake := newFakeCamera()
	cam := &Camera{injected: fake.sdk()}
	require.NoError(t, cam.Connect(context.Background()))
	defer func() { _ = cam.Close() }()

	caps := cam.Caps()
	assert.Equal(t, "ZWO ASI1600MM Pro", caps.Name)
	assert.Equal(t, 4656, caps.MaxWidth)
	assert.Equal(t, 3520, caps.MaxHeight)
	assert.InDelta(t, 3.8, caps.PixelSizeUm, 1e-9)
	assert.Equal(t, []int{1, 2}, caps.Bins)
	assert.True(t, caps.HasCooler, "a camera reporting CoolerOn has a cooler")
	assert.Equal(t, int64(32), caps.MinExposureUs, "the 32 µs floor comes from the camera, not a constant")
	assert.Equal(t, int64(2_000_000_000), caps.MaxExposureUs)
	assert.Equal(t, 16, caps.BitDepth)

	// The frame format must be RAW16 full-frame: throwing four bits away by defaulting to RAW8
	// would quietly halve the dynamic range of every capture.
	assert.Equal(t, int32(imgRaw16), fake.imgType)
	assert.Equal(t, int32(4656), fake.roiW)
}

// The heart of the correctness fix: writing a control must drive the id the CAMERA reported.
// With the old hardcoded table, "cooler_on" would have driven id 9 (ASI_FLIP) instead of 17.
func TestCamera_SetControlDrivesTheIdTheCameraReported(t *testing.T) {
	cam, fake := fakeRig(t)

	require.NoError(t, cam.SetControl(device.ControlCoolerOn, 1, false))
	require.NoError(t, cam.SetControl(device.ControlTargetTemp, -10, false))
	require.NoError(t, cam.SetControl(device.ControlUSBBandwidth, 80, false))

	byID := map[int32]int64{}
	for _, c := range fake.setCalls {
		byID[c.id] = c.value
	}
	assert.Equal(t, int64(1), byID[17], "cooler_on must drive id 17 (CoolerOn), not 9 (Flip)")
	assert.Equal(t, int64(-10), byID[16], "target_temp must drive id 16")
	assert.Equal(t, int64(80), byID[6], "usb_bandwidth must drive id 6 (BandWidth)")
	assert.NotContains(t, byID, int32(9), "nothing may write to Flip")
}

// Every writable control the camera reports must reach the UI, including the ASICAP-parity ones.
func TestCamera_SurfacesEveryReportedControl(t *testing.T) {
	cam, _ := fakeRig(t)

	got := map[string]device.Control{}
	for _, c := range cam.Controls() {
		got[c.Name] = c
	}
	for _, want := range []string{
		device.ControlGain, device.ControlExposure, device.ControlOffset,
		device.ControlUSBBandwidth, device.ControlHighSpeed, device.ControlMonoBin,
		device.ControlFanOn, device.ControlAntiDew,
		device.ControlTemperature, device.ControlTargetTemp,
		device.ControlCoolerOn, device.ControlCoolerPower,
	} {
		assert.Contains(t, got, want, "control %q must be exposed", want)
	}

	assert.Equal(t, int64(600), got[device.ControlGain].Max, "ranges come from the camera")
	assert.False(t, got[device.ControlTemperature].Writable, "temperature is read-only")
	assert.True(t, got[device.ControlGain].AutoSupport)
	assert.Equal(t, int64(10), got[device.ControlTemperature].ScaleDivisor,
		"tenths of a degree must be flagged so the UI does not show -200 °C")
}

// A control the camera does not have must be refused, not written to id zero (which is Gain).
func TestCamera_RefusesControlsTheCameraLacks(t *testing.T) {
	fake := newFakeCamera()
	fake.controls = []fakeControl{
		{name: "Gain", id: 0, min: 0, max: 600, writable: true},
		{name: "Exposure", id: 1, min: 32, max: 1_000_000, writable: true},
	}
	fake.values = map[int32]int64{0: 0, 1: 1000}
	fake.autos = map[int32]int32{}
	cam := &Camera{injected: fake.sdk()}
	require.NoError(t, cam.Connect(context.Background()))
	defer func() { _ = cam.Close() }()

	err := cam.SetControl(device.ControlCoolerOn, 1, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not have")
	assert.Empty(t, fake.setCalls, "a missing control must write nothing at all")
	assert.False(t, cam.Caps().HasCooler)
}

// A slider at its stop should mean "as far as it goes", not a failed capture.
func TestCamera_ClampsToTheReportedRange(t *testing.T) {
	cam, fake := fakeRig(t)

	require.NoError(t, cam.SetControl(device.ControlGain, 99999, false))
	require.NoError(t, cam.SetControl(device.ControlOffset, -5, false))

	byID := map[int32]int64{}
	for _, c := range fake.setCalls {
		byID[c.id] = c.value
	}
	assert.Equal(t, int64(600), byID[0], "gain clamped to the camera's maximum")
	assert.Equal(t, int64(0), byID[5], "offset clamped to the camera's minimum")
}

func TestCamera_RefusesToWriteAReadOnlyControl(t *testing.T) {
	cam, fake := fakeRig(t)
	err := cam.SetControl(device.ControlTemperature, 0, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
	assert.Empty(t, fake.setCalls)
}

// The SDK rejects a width that is not a multiple of 8 or a height not a multiple of 2. Rounding
// here rather than passing the request through means a subframe drag cannot fail mid-session.
func TestCamera_RoundsROIToWhatTheSDKAccepts(t *testing.T) {
	cam, fake := fakeRig(t)

	got, err := cam.SetROI(device.ROI{Width: 61, Height: 33, Bin: 1})
	require.NoError(t, err)
	assert.Equal(t, 56, got.Width, "rounded down to a multiple of 8")
	assert.Equal(t, 32, got.Height, "rounded down to a multiple of 2")
	assert.Equal(t, int32(56), fake.roiW)
	assert.Equal(t, int32(32), fake.roiH)

	// Zero means "full frame at this binning".
	got, err = cam.SetROI(device.ROI{Bin: 2})
	require.NoError(t, err)
	assert.Equal(t, 32, got.Width)
	assert.Equal(t, 24, got.Height)
	assert.Equal(t, 2, got.Bin)

	_, err = cam.SetROI(device.ROI{Width: 4, Height: 1, Bin: 1})
	assert.Error(t, err, "a ROI that rounds to nothing must be refused")
}

func TestCamera_ExposureLifecycle(t *testing.T) {
	cam, fake := fakeRig(t)

	st, err := cam.ExposureState()
	require.NoError(t, err)
	assert.Equal(t, device.ExposureIdle, st)

	require.NoError(t, cam.SetControl(device.ControlExposure, 1000, false))
	require.NoError(t, cam.StartExposure(context.Background(), false))

	st, err = cam.ExposureState()
	require.NoError(t, err)
	assert.Equal(t, device.ExposureWorking, st)

	fake.finishExposure()
	st, err = cam.ExposureState()
	require.NoError(t, err)
	assert.Equal(t, device.ExposureSuccess, st)

	frame, err := cam.Download(context.Background())
	require.NoError(t, err)
	require.NotNil(t, frame)
	assert.Equal(t, 64, frame.Width)
	assert.Equal(t, 48, frame.Height)
	assert.Len(t, frame.Pix, 64*48)
	assert.Equal(t, uint16(0), frame.Pix[0], "the ramp the fake wrote")
	assert.Equal(t, uint16(1), frame.Pix[1])
	assert.Equal(t, int64(1000), frame.ExposureUs)
	assert.True(t, frame.HasTemp, "the sensor temperature must be recorded with the frame")
	// The camera reports 200 tenths of a degree = 20.0 °C, which the engine stores as milli-°C.
	assert.Equal(t, 20000, frame.TempMilliC)
}

// The download buffer is reused between frames for speed, so the pixels handed out must be a COPY.
// Without it, a frame would change under the caller while it was being stacked.
func TestCamera_DownloadReturnsACopyNotAViewOfTheReusedBuffer(t *testing.T) {
	cam, fake := fakeRig(t)
	require.NoError(t, cam.StartExposure(context.Background(), false))
	fake.finishExposure()

	first, err := cam.Download(context.Background())
	require.NoError(t, err)
	firstValue := first.Pix[10]

	// A second download overwrites the internal buffer.
	require.NoError(t, cam.StartExposure(context.Background(), false))
	fake.finishExposure()
	second, err := cam.Download(context.Background())
	require.NoError(t, err)

	assert.Equal(t, firstValue, first.Pix[10], "the first frame must be untouched by the second")
	assert.NotSame(t, &first.Pix[0], &second.Pix[0], "each frame must own its pixels")
}

// Video and still modes are mutually exclusive on ZWO hardware; the driver must say so rather than
// let the SDK fail deep inside a sequence.
func TestCamera_VideoMode(t *testing.T) {
	cam, _ := fakeRig(t)

	assert.False(t, cam.Streaming())
	require.NoError(t, cam.StartVideo(context.Background()))
	assert.True(t, cam.Streaming())

	frame, err := cam.NextFrame(context.Background(), time.Second)
	require.NoError(t, err)
	assert.Equal(t, uint16(7), frame.Pix[0])
	assert.Equal(t, 3, cam.DroppedFrames())

	assert.ErrorIs(t, cam.StartExposure(context.Background(), false), device.ErrBusy,
		"a still exposure during video capture must be refused up front")
	_, err = cam.SetROI(device.ROI{Width: 32, Height: 16, Bin: 1})
	assert.ErrorIs(t, err, device.ErrBusy, "changing the ROI mid-stream would misalign every frame")

	require.NoError(t, cam.StopVideo())
	assert.False(t, cam.Streaming())
	require.NoError(t, cam.StartExposure(context.Background(), false))
}

func TestCamera_NextFrameNeedsVideoRunning(t *testing.T) {
	cam, _ := fakeRig(t)
	_, err := cam.NextFrame(context.Background(), time.Second)
	assert.ErrorIs(t, err, device.ErrBusy)
}

func TestCamera_OperationsRequireAConnection(t *testing.T) {
	cam := New()
	assert.ErrorIs(t, cam.SetControl(device.ControlGain, 1, false), device.ErrNotConnected)
	assert.ErrorIs(t, cam.StartExposure(context.Background(), false), device.ErrNotConnected)
	_, err := cam.Download(context.Background())
	assert.ErrorIs(t, err, device.ErrNotConnected)
	assert.Nil(t, cam.Controls())
	assert.Zero(t, cam.DroppedFrames())
	assert.NoError(t, cam.Close(), "closing an unconnected camera is harmless")
}

func TestCamera_ConnectFailsWithNoCamera(t *testing.T) {
	fake := newFakeCamera()
	fake.cameras = 0
	cam := &Camera{injected: fake.sdk()}
	err := cam.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ZWO camera")
	assert.False(t, cam.Connected())
}

// If the camera cannot be put into the full-frame 16-bit format there is no point continuing, and
// leaving it open would leak the USB handle.
func TestCamera_ConnectClosesTheCameraIfTheFormatIsRejected(t *testing.T) {
	fake := newFakeCamera()
	fake.failROI = true
	cam := &Camera{injected: fake.sdk()}

	err := cam.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "16-bit format")
	assert.False(t, fake.opened, "the camera must not be left open after a failed connect")
	assert.False(t, cam.Connected())
}

func TestCamera_CloseStopsVideo(t *testing.T) {
	cam, fake := fakeRig(t)
	require.NoError(t, cam.StartVideo(context.Background()))
	require.NoError(t, cam.Close())
	assert.False(t, fake.streaming, "closing must stop the stream, not leave the sensor running")
	assert.False(t, fake.opened)
}

// The camera half of the same contract: an unplugged camera must pause the session, not end it.
func TestCheck_AVanishedCameraIsNotConnected(t *testing.T) {
	tests := []struct {
		name         string
		code         int32
		notConnected bool
	}{
		{"camera closed", 4, true},
		{"camera removed", 5, true},
		{"a timeout is a camera that is still there", 11, false},
		{"video mode active", 15, false},
		{"success is no error", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := check(tt.code)
			if tt.code == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.notConnected, errors.Is(err, device.ErrNotConnected))
		})
	}
}
