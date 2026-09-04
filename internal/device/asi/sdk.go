// Package asi drives ZWO ASI cameras through the vendor's C SDK, bound at runtime with purego.
//
// Binding at runtime rather than with cgo keeps CGO_ENABLED=0 for the whole engine, which is what
// lets the Docker image build statically and the sidecar cross-compile. It also makes a missing SDK
// a soft failure — the camera driver simply reports itself unavailable, exactly as GraXpert and
// StarNet do — instead of a build error on machines that have no ZWO hardware.
//
// The SDK is NOT vendored. It is ZWO's own binary, and the project's rule for every external tool
// is to point an environment variable at the user's install.
//
// # The ABI, and why it is decoded by hand
//
// purego does not know C struct layouts. Passing a Go struct and hoping the fields line up is the
// single most likely way to get this wrong, and the failure is silent: fields read as garbage that
// often looks plausible (a sensor width of 4656 read at the wrong offset becomes a plausible-looking
// number, not an obvious error). So every struct is decoded from a byte buffer at explicit offsets
// computed from the C declaration, and a self-test at startup checks the values are sane before any
// of them are trusted.
//
// Note for darwin/linux: C `long` is 64-bit on both, and ZWO's headers use `long` for control values.
package asi

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Error codes from ASICamera2.h. Only the ones worth naming in a message are listed.
type asiError int32

const (
	asiSuccess                 asiError = 0
	asiErrorInvalidIndex       asiError = 1
	asiErrorInvalidID          asiError = 2
	asiErrorCameraClosed       asiError = 4
	asiErrorCameraRemoved      asiError = 5
	asiErrorTimeout            asiError = 11
	asiErrorBufferTooSmall     asiError = 14
	asiErrorVideoModeActive    asiError = 15
	asiErrorExposureInProgress asiError = 16
)

func (e asiError) Error() string {
	switch e {
	case asiErrorInvalidIndex:
		return "asi: no camera at that index"
	case asiErrorInvalidID:
		return "asi: invalid camera id"
	case asiErrorCameraClosed:
		return "asi: the camera is closed"
	case asiErrorCameraRemoved:
		return "asi: the camera was unplugged"
	case asiErrorTimeout:
		return "asi: the camera timed out"
	case asiErrorBufferTooSmall:
		return "asi: the frame buffer is too small"
	case asiErrorVideoModeActive:
		return "asi: video mode is active; stop it before taking a still"
	case asiErrorExposureInProgress:
		return "asi: an exposure is already in progress"
	default:
		return fmt.Sprintf("asi: SDK error %d", int32(e))
	}
}

// check turns an SDK return code into an error.
//
// A camera that has gone away is reported as NOT CONNECTED, for the reason efwCheck gives: the
// sequencer waits out a device error and carries on, but it recognises one by the code devsrv
// derives from this sentinel. Unwrapped, an unplugged camera ends the night instead of pausing it.
func check(code int32) error {
	err := asiError(code)
	switch err {
	case asiSuccess:
		return nil
	case asiErrorCameraClosed, asiErrorCameraRemoved:
		return fmt.Errorf("%w: %w", device.ErrNotConnected, err)
	default:
		return err
	}
}

// ASICameraInfo, from ASICamera2.h. The offsets are computed from the C declaration, with the
// natural alignment a C compiler applies on 64-bit darwin and linux:
//
//	char Name[64];              // 0
//	int  CameraID;              // 64
//	long MaxHeight;             // 72  (8-aligned, so 4 bytes of padding at 68)
//	long MaxWidth;              // 80
//	ASI_BOOL IsColorCam;        // 88
//	ASI_BAYER_PATTERN BayerPattern; // 92
//	int  SupportedBins[16];     // 96
//	ASI_IMG_TYPE SupportedVideoFormat[8]; // 160
//	double PixelSize;           // 192
//	ASI_BOOL MechanicalShutter; // 200
//	ASI_BOOL IsCoolerCam;       // 204  (after ST4Port at 204? see below)
//
// The trailing booleans are read individually below rather than as a block, because their order
// differs between SDK versions and a mis-read there would silently disable cooling.
const (
	infoStructSize   = 256 // generous: the struct is 232 bytes in 1.41; extra bytes are harmless
	offName          = 0
	offCameraID      = 64
	offMaxHeight     = 72
	offMaxWidth      = 80
	offIsColorCam    = 88
	offBayerPattern  = 92
	offSupportedBins = 96
	offPixelSize     = 192
)

// ASI_CONTROL_CAPS, from ASICamera2.h:
//
//	char Name[64];              // 0
//	char Description[128];      // 64
//	long MaxValue;              // 192
//	long MinValue;              // 200
//	long DefaultValue;          // 208
//	ASI_BOOL IsAutoSupported;   // 216
//	ASI_BOOL IsWritable;        // 220
//	ASI_CONTROL_TYPE ControlType; // 224
const (
	capsStructSize     = 288
	offCapsName        = 0
	offCapsDescription = 64
	offCapsMax         = 192
	offCapsMin         = 200
	offCapsDefault     = 208
	offCapsAutoSupport = 216
	offCapsWritable    = 220
	offCapsControlType = 224
)

// There is deliberately NO table of control ids here.
//
// ASICamera2.h numbers its controls, and hardcoding those numbers looks like the obvious thing to
// do. It is a trap: the enum has grown between SDK versions, the header does not ship with the
// binary library, and an id that is off by one does not fail — it drives the wrong control. The
// driver instead reads each control's id from the camera's own ASIGetControlCaps report. See
// controls.go.

// Image formats.
const (
	imgRaw8  = 0
	imgRGB24 = 1
	imgRaw16 = 2
	imgY8    = 3
)

// Exposure status.
const (
	expIdle    = 0
	expWorking = 1
	expSuccess = 2
	expFailed  = 3
)

// sdk holds the resolved function pointers.
type sdk struct {
	getNumOfConnectedCameras func() int32
	getCameraProperty        func(info *byte, index int32) int32
	openCamera               func(id int32) int32
	initCamera               func(id int32) int32
	closeCamera              func(id int32) int32
	getNumOfControls         func(id int32, n *int32) int32
	getControlCaps           func(id int32, index int32, caps *byte) int32
	getControlValue          func(id int32, ctrl int32, value *int64, auto *int32) int32
	setControlValue          func(id int32, ctrl int32, value int64, auto int32) int32
	setROIFormat             func(id, w, h, bin, imgType int32) int32
	getROIFormat             func(id int32, w, h, bin, imgType *int32) int32
	setStartPos              func(id, x, y int32) int32
	getStartPos              func(id int32, x, y *int32) int32
	startExposure            func(id int32, isDark int32) int32
	stopExposure             func(id int32) int32
	getExpStatus             func(id int32, status *int32) int32
	getDataAfterExp          func(id int32, buf *byte, size int64) int32
	startVideoCapture        func(id int32) int32
	stopVideoCapture         func(id int32) int32
	getVideoData             func(id int32, buf *byte, size int64, waitMs int32) int32
	getSDKVersion            func() uintptr
	getDroppedFrames         func(id int32, dropped *int32) int32
}

var (
	loadOnce sync.Once
	loaded   *sdk
	loadErr  error
)

// load resolves the SDK once. Failure is remembered, so a missing library is reported the same way
// every time rather than retried on each connect.
func load() (*sdk, error) {
	loadOnce.Do(func() {
		path, err := findLibrary()
		if err != nil {
			loadErr = err
			return
		}
		handle, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = explainLoadFailure(path, err)
			return
		}
		s := &sdk{}
		defer func() {
			// A missing symbol makes purego panic. Turning that into an error keeps a partial or
			// wrong-architecture library from taking the whole device server down.
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("asi: %s is not a usable ASICamera2 library: %v", path, r)
				loaded = nil
			}
		}()
		register(handle, s)
		if err := selfTest(s); err != nil {
			loadErr = err
			return
		}
		loaded = s
	})
	if loaded == nil && loadErr == nil {
		loadErr = fmt.Errorf("asi: the SDK could not be loaded")
	}
	return loaded, loadErr
}

// register binds every function this driver uses.
func register(handle uintptr, s *sdk) {
	purego.RegisterLibFunc(&s.getNumOfConnectedCameras, handle, "ASIGetNumOfConnectedCameras")
	purego.RegisterLibFunc(&s.getCameraProperty, handle, "ASIGetCameraProperty")
	purego.RegisterLibFunc(&s.openCamera, handle, "ASIOpenCamera")
	purego.RegisterLibFunc(&s.initCamera, handle, "ASIInitCamera")
	purego.RegisterLibFunc(&s.closeCamera, handle, "ASICloseCamera")
	purego.RegisterLibFunc(&s.getNumOfControls, handle, "ASIGetNumOfControls")
	purego.RegisterLibFunc(&s.getControlCaps, handle, "ASIGetControlCaps")
	purego.RegisterLibFunc(&s.getControlValue, handle, "ASIGetControlValue")
	purego.RegisterLibFunc(&s.setControlValue, handle, "ASISetControlValue")
	purego.RegisterLibFunc(&s.setROIFormat, handle, "ASISetROIFormat")
	purego.RegisterLibFunc(&s.getROIFormat, handle, "ASIGetROIFormat")
	purego.RegisterLibFunc(&s.setStartPos, handle, "ASISetStartPos")
	purego.RegisterLibFunc(&s.getStartPos, handle, "ASIGetStartPos")
	purego.RegisterLibFunc(&s.startExposure, handle, "ASIStartExposure")
	purego.RegisterLibFunc(&s.stopExposure, handle, "ASIStopExposure")
	purego.RegisterLibFunc(&s.getExpStatus, handle, "ASIGetExpStatus")
	purego.RegisterLibFunc(&s.getDataAfterExp, handle, "ASIGetDataAfterExp")
	purego.RegisterLibFunc(&s.startVideoCapture, handle, "ASIStartVideoCapture")
	purego.RegisterLibFunc(&s.stopVideoCapture, handle, "ASIStopVideoCapture")
	purego.RegisterLibFunc(&s.getVideoData, handle, "ASIGetVideoData")
	purego.RegisterLibFunc(&s.getSDKVersion, handle, "ASIGetSDKVersion")
	purego.RegisterLibFunc(&s.getDroppedFrames, handle, "ASIGetDroppedFrames")
}

// selfTest checks the binding before anything trusts it.
//
// This exists because a wrong struct offset produces plausible-looking numbers rather than an
// obvious crash. Reading a camera's properties and sanity-checking them is the cheapest way to
// catch an ABI mismatch — a real ASI sensor is between 640 and 20000 pixels wide with pixels
// between 1 and 25 µm, and anything outside that means the offsets are wrong for this SDK version.
func selfTest(s *sdk) error {
	if s.getSDKVersion == nil {
		return fmt.Errorf("asi: the library did not export ASIGetSDKVersion")
	}
	n := s.getNumOfConnectedCameras()
	if n < 0 || n > 64 {
		return fmt.Errorf("asi: the SDK reported an implausible camera count (%d) — "+
			"the library is probably the wrong architecture", n)
	}
	if n == 0 {
		return nil // nothing plugged in is not a binding failure
	}
	buf := make([]byte, infoStructSize)
	if err := check(s.getCameraProperty(&buf[0], 0)); err != nil {
		return fmt.Errorf("asi: self-test could not read camera 0: %w", err)
	}
	info := parseCameraInfo(buf)
	if info.MaxWidth < 640 || info.MaxWidth > 20000 || info.MaxHeight < 480 || info.MaxHeight > 20000 {
		return fmt.Errorf("asi: self-test read an implausible sensor size (%dx%d) — "+
			"the ASICameraInfo layout does not match this SDK version",
			info.MaxWidth, info.MaxHeight)
	}
	if info.PixelSizeUm < 1 || info.PixelSizeUm > 25 {
		return fmt.Errorf("asi: self-test read an implausible pixel size (%.2f µm) — "+
			"the ASICameraInfo layout does not match this SDK version", info.PixelSizeUm)
	}
	return nil
}

// CameraInfo is the decoded ASICameraInfo.
type CameraInfo struct {
	Name         string
	ID           int32
	MaxWidth     int64
	MaxHeight    int64
	IsColor      bool
	BayerPattern int32
	Bins         []int
	PixelSizeUm  float64
}

// parseCameraInfo decodes the struct by explicit offsets — never by casting a Go struct over it.
func parseCameraInfo(b []byte) CameraInfo {
	if len(b) < offPixelSize+8 {
		return CameraInfo{}
	}
	info := CameraInfo{
		Name:         cString(b[offName : offName+64]),
		ID:           int32(binary.LittleEndian.Uint32(b[offCameraID:])),
		MaxHeight:    int64(binary.LittleEndian.Uint64(b[offMaxHeight:])),
		MaxWidth:     int64(binary.LittleEndian.Uint64(b[offMaxWidth:])),
		IsColor:      binary.LittleEndian.Uint32(b[offIsColorCam:]) != 0,
		BayerPattern: int32(binary.LittleEndian.Uint32(b[offBayerPattern:])),
		PixelSizeUm:  float64FromBits(binary.LittleEndian.Uint64(b[offPixelSize:])),
	}
	// SupportedBins is a zero-terminated list of up to 16 ints.
	for i := 0; i < 16; i++ {
		v := int32(binary.LittleEndian.Uint32(b[offSupportedBins+i*4:]))
		if v == 0 {
			break
		}
		info.Bins = append(info.Bins, int(v))
	}
	return info
}

// ControlCaps is the decoded ASI_CONTROL_CAPS. Every range the UI shows comes from here rather than
// from a hardcoded table — the often-quoted "gain 0–600" is community lore, and a camera that
// disagrees would silently have its controls clamped wrongly.
type ControlCaps struct {
	Name        string
	Description string
	Min, Max    int64
	Default     int64
	AutoSupport bool
	Writable    bool
	ControlType int32
}

func parseControlCaps(b []byte) ControlCaps {
	if len(b) < offCapsControlType+4 {
		return ControlCaps{}
	}
	return ControlCaps{
		Name:        cString(b[offCapsName : offCapsName+64]),
		Description: cString(b[offCapsDescription : offCapsDescription+128]),
		Max:         int64(binary.LittleEndian.Uint64(b[offCapsMax:])),
		Min:         int64(binary.LittleEndian.Uint64(b[offCapsMin:])),
		Default:     int64(binary.LittleEndian.Uint64(b[offCapsDefault:])),
		AutoSupport: binary.LittleEndian.Uint32(b[offCapsAutoSupport:]) != 0,
		Writable:    binary.LittleEndian.Uint32(b[offCapsWritable:]) != 0,
		ControlType: int32(binary.LittleEndian.Uint32(b[offCapsControlType:])),
	}
}

// cString reads a NUL-terminated string out of a fixed-width C char array.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// float64FromBits decodes an IEEE-754 double out of the struct buffer.
func float64FromBits(bits uint64) float64 {
	return math.Float64frombits(bits)
}

// findLibrary locates libASICamera2, from the env var first and then the usual install paths.
func findLibrary() (string, error) {
	if p := os.Getenv("ASI_SDK_LIB"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("asi: ASI_SDK_LIB points at %s, which does not exist", p)
		}
		return p, nil
	}
	name := "libASICamera2.so"
	if runtime.GOOS == "darwin" {
		name = "libASICamera2.dylib"
	}
	for _, dir := range []string{
		"/usr/local/lib", "/opt/homebrew/lib", "/usr/lib",
		"/Applications/ASIStudio.app/Contents/Frameworks",
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf(
		"asi: %s not found — install the ZWO SDK and set ASI_SDK_LIB to the library path", name)
}

// Available reports whether the SDK can be used, for the driver report.
func Available() (string, error) {
	s, err := load()
	if err != nil {
		return "", err
	}
	n := s.getNumOfConnectedCameras()
	if n == 0 {
		return "SDK loaded; no camera connected", nil
	}
	return fmt.Sprintf("SDK loaded; %d camera(s) connected", n), nil
}

// Device names one camera the SDK can see, as discovery reports it.
type Device struct {
	Index int32
	ID    int32
	Name  string
}

// List enumerates the connected cameras WITHOUT opening any of them.
//
// Not opening is the whole point. Discovery is polled by the UI, and ASIOpenCamera on a camera that
// is already mid-sequence would take it away from the exposure in flight. ASIGetCameraProperty
// answers from the enumeration alone, so this is safe to run on a timer.
func List() ([]Device, error) {
	s, err := load()
	if err != nil {
		return nil, err
	}
	return listCameras(s), nil
}

// listCameras is split from List so the fake SDK can drive it with no vendor library present.
func listCameras(s *sdk) []Device {
	n := s.getNumOfConnectedCameras()
	if n <= 0 || n > 64 {
		// An implausible count means the wrong architecture; selfTest is what reports that properly.
		return nil
	}
	out := make([]Device, 0, n)
	buf := make([]byte, infoStructSize)
	for i := int32(0); i < n; i++ {
		if err := check(s.getCameraProperty(&buf[0], i)); err != nil {
			continue // a camera unplugged between the count and the read must not lose the others
		}
		info := parseCameraInfo(buf)
		out = append(out, Device{Index: i, ID: info.ID, Name: info.Name})
	}
	return out
}
