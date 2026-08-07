// Package device is the hardware layer: camera, filter wheel and mount, behind interfaces thin
// enough that a simulator, a vendor SDK bound at runtime, or a future INDI/Alpaca bridge all satisfy
// them identically.
//
// Everything here is deliberately driver-agnostic and free of I/O policy: no file naming, no
// sequencing, no database. The engine owns those (it knows about targets, mosaic tiles and nights);
// a driver only moves hardware and hands back pixels. That split is what lets the whole capture
// stack — sequencer, focus meter, plate-solve centring — be developed and tested against the
// simulator with no hardware attached.
package device

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors callers branch on.
var (
	// ErrNotConnected is returned by every operation on a device that has not been opened.
	ErrNotConnected = errors.New("device not connected")
	// ErrUnsupported marks a capability this particular hardware does not have (no cooler, no
	// filter wheel). Callers degrade rather than fail.
	ErrUnsupported = errors.New("not supported by this device")
	// ErrBusy is returned when an operation collides with one already in progress (an exposure
	// while another is running, a filter move while the wheel is turning).
	ErrBusy = errors.New("device busy")
	// ErrDriverUnavailable means the driver itself could not be loaded — a missing vendor SDK, no
	// serial port. The UI shows this as "not installed", never as a crash.
	ErrDriverUnavailable = errors.New("driver unavailable")
)

// Info identifies a discovered device.
type Info struct {
	ID     string `json:"id"`     // driver-scoped identifier, stable across reconnects when possible
	Name   string `json:"name"`   // human name as the driver reports it
	Driver string `json:"driver"` // "asi", "efw", "nexstar", "sim"
	Kind   string `json:"kind"`   // "camera", "wheel", "mount"
}

// Device kinds.
const (
	KindCamera = "camera"
	KindWheel  = "wheel"
	KindMount  = "mount"
)

// Control is one settable camera parameter, discovered from the hardware rather than hardcoded.
// Every range in the UI comes from here: cameras differ, firmware changes ranges, and a hardcoded
// "gain 0–600" is a bug waiting for a different model to be plugged in.
type Control struct {
	Name         string `json:"name"` // canonical key: "gain", "exposure", "offset", "temperature", …
	Label        string `json:"label"`
	Min          int64  `json:"min"`
	Max          int64  `json:"max"`
	Default      int64  `json:"default"`
	Value        int64  `json:"value"`
	Writable     bool   `json:"writable"`
	AutoSupport  bool   `json:"auto_supported"`
	Auto         bool   `json:"auto"`
	Unit         string `json:"unit,omitempty"` // "µs", "°C", "%" …
	Description  string `json:"description,omitempty"`
	ScaleDivisor int64  `json:"scale_divisor,omitempty"` // reported value ÷ this = real units
}

// Well-known control names. Drivers map their vendor enums onto these so the UI (and the sequencer)
// never speak a vendor dialect.
const (
	ControlGain         = "gain"
	ControlExposure     = "exposure" // microseconds
	ControlOffset       = "offset"
	ControlTemperature  = "temperature"  // read-only sensor temperature
	ControlTargetTemp   = "target_temp"  // cooler setpoint
	ControlCoolerOn     = "cooler_on"    // 0/1
	ControlCoolerPower  = "cooler_power" // read-only %
	ControlFanOn        = "fan_on"
	ControlAntiDew      = "anti_dew"
	ControlUSBBandwidth = "usb_bandwidth"
	ControlHighSpeed    = "high_speed"
	ControlMonoBin      = "mono_bin"
	ControlHardwareBin  = "hardware_bin"
	ControlGamma        = "gamma"
	ControlFlip         = "flip"
	// Colour-camera controls. A mono sensor never reports these, so they simply do not appear.
	ControlWBRed  = "wb_red"
	ControlWBBlue = "wb_blue"
	// Auto-exposure ceilings and the overclock knob: rarely touched, but a camera that offers them
	// should not have them hidden.
	ControlOverclock            = "overclock"
	ControlAutoMaxGain          = "auto_max_gain"
	ControlAutoMaxExp           = "auto_max_exp"
	ControlAutoTargetBrightness = "auto_target_brightness"
)

// AdvancedControls are the ones the UI tucks away behind a disclosure: real, but not part of the
// nightly routine. Everything else the camera reports is shown up front.
var AdvancedControls = map[string]bool{
	ControlFanOn: true, ControlAntiDew: true, ControlMonoBin: true, ControlHardwareBin: true,
	ControlGamma: true, ControlFlip: true, ControlOverclock: true,
	ControlAutoMaxGain: true, ControlAutoMaxExp: true, ControlAutoTargetBrightness: true,
	ControlWBRed: true, ControlWBBlue: true,
}

// CameraCaps is the fixed description of a camera: what it is, not how it is currently set.
type CameraCaps struct {
	Info
	MaxWidth      int      `json:"max_width"`
	MaxHeight     int      `json:"max_height"`
	PixelSizeUm   float64  `json:"pixel_size_um"`
	BitDepth      int      `json:"bit_depth"`
	IsColor       bool     `json:"is_color"`
	BayerPattern  string   `json:"bayer_pattern,omitempty"`
	HasCooler     bool     `json:"has_cooler"`
	HasShutter    bool     `json:"has_shutter"`
	HasST4        bool     `json:"has_st4"`
	Bins          []int    `json:"bins"`
	ImageTypes    []string `json:"image_types"` // "raw8", "raw16", "rgb24", "y8"
	ElecPerADU    float64  `json:"elec_per_adu,omitempty"`
	SerialNumber  string   `json:"serial_number,omitempty"`
	SDKVersion    string   `json:"sdk_version,omitempty"`
	MinExposureUs int64    `json:"min_exposure_us"`
	MaxExposureUs int64    `json:"max_exposure_us"`
}

// ROI is the read-out region, in BINNED pixels. Width must be a multiple of 8 and height of 2 for
// ZWO hardware; drivers round and report what they actually applied.
type ROI struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bin    int    `json:"bin"`
	Format string `json:"format"` // "raw16" (default), "raw8", "rgb24", "y8"
}

// Frame is one downloaded exposure. Pix is row-major, top-down, one channel; 8-bit formats are
// promoted to the same 0…65535 range so everything downstream sees one representation.
type Frame struct {
	Width      int
	Height     int
	Bin        int
	Pix        []uint16
	ExposureUs int64
	Gain       int64
	Offset     int64
	TempMilliC int
	HasTemp    bool
	StartedAt  time.Time // exposure START (DATE-OBS is the start of integration by convention)
	Duration   time.Duration
	// ExtraCards are FITS header cards only the driver could know, appended after the standard capture
	// header. Real cameras leave this nil; the simulator uses it to record the truth about a frame it
	// invented, which is what lets the plate-solve-driven features be exercised without a sky.
	ExtraCards []string
}

// ExposureState is the lifecycle of one still exposure.
type ExposureState string

const (
	ExposureIdle    ExposureState = "idle"
	ExposureWorking ExposureState = "working"
	ExposureSuccess ExposureState = "success"
	ExposureFailed  ExposureState = "failed"
)

// Camera is a still + video imaging device. Implementations must be safe for concurrent use: the
// control plane (settings, status polling) runs on a different goroutine from the capture loop.
type Camera interface {
	Connect(ctx context.Context) error
	Close() error
	Connected() bool

	Caps() CameraCaps
	Controls() []Control
	SetControl(name string, value int64, auto bool) error
	Control(name string) (Control, bool)

	ROI() ROI
	SetROI(roi ROI) (ROI, error)

	// StartExposure begins one still exposure. dark tells the driver a shutter should stay closed;
	// it is a no-op on shutterless cameras.
	StartExposure(ctx context.Context, dark bool) error
	ExposureState() (ExposureState, error)
	AbortExposure() error
	// Download fetches the pixels of a completed exposure.
	Download(ctx context.Context) (*Frame, error)

	// StartVideo/NextFrame/StopVideo are the fast preview + planetary path. Video and still mode
	// are mutually exclusive on ZWO hardware, so a driver returns ErrBusy rather than fighting.
	StartVideo(ctx context.Context) error
	NextFrame(ctx context.Context, timeout time.Duration) (*Frame, error)
	StopVideo() error
	Streaming() bool
	DroppedFrames() int
}

// WheelState reports where a filter wheel is.
type WheelState struct {
	Info
	Slots    int      `json:"slots"`
	Position int      `json:"position"` // 1-based slot; 0 while moving
	Moving   bool     `json:"moving"`
	Names    []string `json:"names,omitempty"` // slot → filter name, index 0 = slot 1
}

// FilterWheel is a motorised filter wheel. SetPosition is asynchronous: it returns as soon as the
// move starts, and State reports Moving until it lands.
type FilterWheel interface {
	Connect(ctx context.Context) error
	Close() error
	Connected() bool
	State() (WheelState, error)
	SetPosition(slot int) error
	// WaitSettled blocks until the wheel stops moving or ctx is done — the sequencer's "don't
	// expose through a half-open filter" guard.
	WaitSettled(ctx context.Context) error
	Calibrate(ctx context.Context) error
	// SetFilterNames records what is physically in each slot, index 0 = slot 1.
	//
	// On the interface rather than duck-typed on purpose: a wheel knows only slot NUMBERS, and this
	// mapping is the only thing that turns one into a FILTER header. Per-filter flats, channel
	// detection and the whole stacking path key off that header, so a driver that quietly lacked
	// this method would degrade every run rather than fail — which is exactly what happened while
	// the check was `interface{ SetFilterNames([]string) }` and one driver spelled it SetNames.
	SetFilterNames(names []string)
}

// MountState is a mount's live pointing and motion status. Coordinates are J2000 degrees — the
// NexStar driver converts to and from the mount's own epoch, so nothing above this layer has to
// think about precession.
type MountState struct {
	Info
	RADeg        float64 `json:"ra_deg"`
	DecDeg       float64 `json:"dec_deg"`
	AltDeg       float64 `json:"alt_deg"`
	AzDeg        float64 `json:"az_deg"`
	Slewing      bool    `json:"slewing"`
	Tracking     bool    `json:"tracking"`
	TrackingRate string  `json:"tracking_rate,omitempty"` // "sidereal" | "lunar" | "solar" | "off"
	Aligned      bool    `json:"aligned"`
	PierSide     string  `json:"pier_side,omitempty"` // "east" | "west" | ""
	Firmware     string  `json:"firmware,omitempty"`
	Model        string  `json:"model,omitempty"`
}

// Direction is a manual-slew axis direction.
type Direction string

const (
	DirNorth Direction = "north"
	DirSouth Direction = "south"
	DirEast  Direction = "east"
	DirWest  Direction = "west"
)

// Mount is a GoTo telescope mount.
type Mount interface {
	Connect(ctx context.Context) error
	Close() error
	Connected() bool
	State(ctx context.Context) (MountState, error)

	// GotoRADec slews to J2000 coordinates. Implementations refuse when the mount is not aligned —
	// an unaligned GoTo points somewhere arbitrary and can drive the tube into the tripod.
	GotoRADec(ctx context.Context, raDeg, decDeg float64) error
	// Sync tells the mount it is currently pointing at these J2000 coordinates (plate-solve
	// centring).
	Sync(ctx context.Context, raDeg, decDeg float64) error
	Abort(ctx context.Context) error

	// Jog starts a manual slew; rate is 1–9 in hand-controller units. Jog with rate 0 stops.
	Jog(ctx context.Context, dir Direction, rate int) error
	// Nudge moves by a small angular amount at guide speed — the dither primitive. Arcseconds.
	Nudge(ctx context.Context, dRAArcsec, dDecArcsec float64) error

	SetTracking(ctx context.Context, on bool, rate string) error
}

// SiderealArcsecPerSec is the rate the sky turns, and the unit a mount's PEC rates are scaled
// against. It lives here because the driver and the simulator must agree on it exactly — two copies
// that differ in the fifth decimal would show up as a slow phase creep nobody could explain.
const SiderealArcsecPerSec = 15.0410686

// GuideAxis names the mount axis a guide correction addresses.
type GuideAxis int

const (
	GuideAxisRA GuideAxis = iota
	GuideAxisDec
)

func (a GuideAxis) String() string {
	if a == GuideAxisDec {
		return "dec"
	}
	return "ra"
}

// GuideMount is a mount that can turn one axis at a commanded rate for a commanded time.
//
// Deliberately NOT part of Mount, for the same reason PECMount is not: a mount that cannot do this
// should make the absence visible rather than stub a method that silently does nothing. Callers
// type-assert and degrade.
//
// # Units: AXIS arcseconds, not sky arcseconds
//
// A rate here is motor rotation, because that is what the wire command is. A rotation of the
// right-ascension axis by A arcseconds moves the sky — and therefore the star on the sensor — by
// A·cos(dec). Implementations must honour that: the simulator converts explicitly, and a driver that
// forgot to would over-correct by 1/cos(dec), which looks like a mount that guides beautifully on the
// celestial equator and oscillates near the pole.
//
// This is also why it does not reuse Mount.Nudge. Nudge is the dither primitive: fixed guide speed,
// both axes at once, and its arguments are tangent-plane offsets rather than axis rotation. Close
// enough for a dither, whose achieved offset is measured afterwards anyway, but not for a servo.
type GuideMount interface {
	// PulseGuide turns one axis at arcsecPerSec for d, then stops it. A negative rate reverses the
	// direction. Implementations must stop the axis even when the context is cancelled: a pulse that
	// never ends is a mount that walks away.
	PulseGuide(ctx context.Context, axis GuideAxis, arcsecPerSec float64, d time.Duration) error
	// GuideRate reports the mount's own configured autoguide rate as a fraction of sidereal, so a
	// caller can size its pulses to what the mount expects instead of hardcoding a speed.
	GuideRate(ctx context.Context) (float64, error)
	// SetGuideRate configures that fraction. Values outside 0…1 are clamped by the driver rather than
	// rejected, because the useful range is a property of the hardware.
	SetGuideRate(ctx context.Context, fraction float64) error
}

// PECCaps describes the shape of a mount's periodic-error-correction table. Every field is read from
// the mount or derived from its model rather than assumed, because the two that vary between models —
// the number of bins and the rate scale — are exactly the two that silently produce a curve of the
// wrong amplitude if guessed.
type PECCaps struct {
	Bins          int     `json:"bins"`
	WormPeriodSec float64 `json:"worm_period_sec"`
	BinSec        float64 `json:"bin_sec"`
	// LSBArcsecPerSec is the rate correction one unit of a bin value represents. Bin values are
	// signed bytes, so the whole table can only ever ask for ±127 of these.
	LSBArcsecPerSec float64 `json:"lsb_arcsec_per_sec"`
}

// PECStatus is where the worm is and what the mount is doing about it.
//
// Indexed and CurrentBin come from the mount. Seeking and Playing are what the driver last commanded:
// the protocol offers no way to ask "are you playing back?", so a mount power-cycled behind our back
// reads as not playing, which is the safe direction to be wrong in.
type PECStatus struct {
	Supported  bool `json:"supported"`
	Indexed    bool `json:"indexed"`
	Seeking    bool `json:"seeking"`
	Playing    bool `json:"playing"`
	CurrentBin int  `json:"current_bin"`
}

// PECMount is a mount that can record and replay a periodic-error correction curve.
//
// This is deliberately NOT part of Mount: plenty of mounts have no PEC table, and forcing them to
// stub six methods would make the absence of the feature invisible. Callers type-assert for it and
// degrade when it is missing.
//
// Curves are slices of exactly PECCaps.Bins signed rate corrections, indexed by worm bin.
type PECMount interface {
	PECCaps(ctx context.Context) (PECCaps, error)
	PECStatus(ctx context.Context) (PECStatus, error)
	// PECSeekIndex starts the hunt for the worm's index mark. It MOVES the mount — up to two degrees
	// in RA — so it belongs before framing, never after.
	PECSeekIndex(ctx context.Context) error
	// PECBin is the bin the worm is turning through right now: the phase reference a training run
	// folds on, in preference to any clock.
	PECBin(ctx context.Context) (int, error)
	PECReadCurve(ctx context.Context) ([]int8, error)
	// PECWriteCurve writes the whole table and verifies every bin read back. Implementations must not
	// report success on a partially written table.
	PECWriteCurve(ctx context.Context, curve []int8) error
	PECPlayback(ctx context.Context, on bool) error
	// PECRecordStop cancels any recording the mount began on its own — a hand-controller record
	// session is invisible over the wire and would overwrite the table while we measure.
	PECRecordStop(ctx context.Context) error
}

// FitFilterNames sizes a slot→filter list to a wheel's ACTUAL slot count: extras are dropped, missing
// entries become empty strings.
//
// It exists because the two are separately sourced and used to disagree. The names are user
// configuration that outlives any one wheel (they are stored server-side and reloaded), while the
// count is a property of the hardware plugged in tonight — a 5-slot EFW and a 7-slot EFW are both
// "the wheel". Left unreconciled, a 7-name configuration applied to a 5-slot wheel made the UI offer
// slots 6 and 7 that physically do not exist, and the driver then refused the move.
//
// Empty strings are kept rather than trimmed: slot 4 being empty is meaningful (nothing fitted), and
// dropping it would silently renumber every filter after it.
func FitFilterNames(names []string, slots int) []string {
	if slots <= 0 {
		return nil
	}
	out := make([]string, slots)
	copy(out, names)
	return out
}
