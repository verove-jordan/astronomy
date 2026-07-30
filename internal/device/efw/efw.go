// Package efw drives ZWO EFW filter wheels through the vendor's C SDK, bound at runtime with
// purego — the same approach, and the same soft-fail rule, as the ASI camera driver.
package efw

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ebitengine/purego"

	"github.com/verove-jordan/astronomy/internal/device"
)

// EFW_INFO, from EFW_filter.h:
//
//	int ID;          // 0
//	char Name[64];   // 4
//	int slotNum;     // 68
const (
	infoStructSize = 80
	offID          = 0
	offName        = 4
	offSlotNum     = 68
)

// movingPosition is what EFWGetPosition returns while the wheel is still turning.
//
// This is the single most important detail in the whole driver: the position call does NOT block,
// and it reports -1 until the wheel settles. Treating that -1 as a slot number would mean the next
// exposure starts mid-rotation, through whatever filter happens to be passing — and the frame would
// look plausible while being unusable.
const movingPosition = -1

type sdk struct {
	getNum        func() int32
	getID         func(index int32, id *int32) int32
	getProperty   func(id int32, info *byte) int32
	open          func(id int32) int32
	close         func(id int32) int32
	getPosition   func(id int32, pos *int32) int32
	setPosition   func(id int32, pos int32) int32
	getSDKVersion func() uintptr
	calibrate     func(id int32) int32
}

var (
	loadOnce sync.Once
	loaded   *sdk
	loadErr  error
)

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
			if r := recover(); r != nil {
				loadErr = fmt.Errorf("efw: %s is not a usable EFW library: %v", path, r)
				loaded = nil
			}
		}()
		purego.RegisterLibFunc(&s.getNum, handle, "EFWGetNum")
		purego.RegisterLibFunc(&s.getID, handle, "EFWGetID")
		purego.RegisterLibFunc(&s.getProperty, handle, "EFWGetProperty")
		purego.RegisterLibFunc(&s.open, handle, "EFWOpen")
		purego.RegisterLibFunc(&s.close, handle, "EFWClose")
		purego.RegisterLibFunc(&s.getPosition, handle, "EFWGetPosition")
		purego.RegisterLibFunc(&s.setPosition, handle, "EFWSetPosition")
		purego.RegisterLibFunc(&s.getSDKVersion, handle, "EFWGetSDKVersion")
		purego.RegisterLibFunc(&s.calibrate, handle, "EFWCalibrate")
		if n := s.getNum(); n < 0 || n > 16 {
			loadErr = fmt.Errorf("efw: the SDK reported an implausible wheel count (%d) — "+
				"the library is probably the wrong architecture", n)
			return
		}
		loaded = s
	})
	if loaded == nil && loadErr == nil {
		loadErr = fmt.Errorf("efw: the SDK could not be loaded")
	}
	return loaded, loadErr
}

func findLibrary() (string, error) {
	if p := os.Getenv("EFW_SDK_LIB"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("efw: EFW_SDK_LIB points at %s, which does not exist", p)
		}
		return p, nil
	}
	name := "libEFWFilter.so"
	if runtime.GOOS == "darwin" {
		name = "libEFWFilter.dylib"
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
		"efw: %s not found — install the ZWO EFW SDK and set EFW_SDK_LIB to the library path", name)
}

// Available reports whether the SDK can be used, for the driver report.
func Available() (string, error) {
	s, err := load()
	if err != nil {
		return "", err
	}
	n := s.getNum()
	if n == 0 {
		return "SDK loaded; no filter wheel connected", nil
	}
	return fmt.Sprintf("SDK loaded; %d wheel(s) connected", n), nil
}

// Wheel implements device.FilterWheel.
type Wheel struct {
	mu  sync.Mutex
	sdk *sdk
	// injected replaces the vendor library in tests; nil in every real build.
	injected  *sdk
	id        int32
	name      string
	slots     int
	connected bool
	names     []string
}

func New() *Wheel { return &Wheel{} }

// resolveSDK returns the vendor library, or an injected stand-in for tests. Without this seam the
// wheel's settle logic — the part that stops an exposure starting mid-rotation — could not be
// tested on a machine with no ZWO library and no hardware.
func (w *Wheel) resolveSDK() (*sdk, error) {
	if w.injected != nil {
		return w.injected, nil
	}
	return load()
}

func (w *Wheel) Connect(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.connected {
		return nil
	}
	s, err := w.resolveSDK()
	if err != nil {
		return err
	}
	if s.getNum() <= 0 {
		return fmt.Errorf("efw: no ZWO filter wheel is connected")
	}
	var id int32
	if err := efwCheck("EFWGetID", s.getID(0, &id)); err != nil {
		return err
	}
	if err := efwCheck("EFWOpen", s.open(id)); err != nil {
		return err
	}
	buf := make([]byte, infoStructSize)
	if err := efwCheck("EFWGetProperty", s.getProperty(id, &buf[0])); err != nil {
		_ = s.close(id)
		return err
	}
	slots := int(int32(binary.LittleEndian.Uint32(buf[offSlotNum:])))
	if slots <= 0 || slots > 16 {
		_ = s.close(id)
		return fmt.Errorf("efw: the wheel reported %d slots, which cannot be right — "+
			"the EFW_INFO layout does not match this SDK version", slots)
	}
	w.sdk, w.id, w.connected = s, id, true
	w.name = cString(buf[offName : offName+64])
	w.slots = slots
	return nil
}

func (w *Wheel) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return nil
	}
	err := efwCheck("EFWClose", w.sdk.close(w.id))
	w.connected = false
	return err
}

func (w *Wheel) Connected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.connected
}

func (w *Wheel) Slots() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.slots
}

// Names returns the configured filter names, one per slot.
func (w *Wheel) Names() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

// SetFilterNames records what is actually in each slot. The wheel itself has no idea — it only knows
// slot numbers — so the mapping comes from the user and is what makes FILTER headers meaningful.
func (w *Wheel) SetFilterNames(names []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Fitted to what this wheel actually has. A saved 7-filter configuration applied to a 5-slot
	// wheel would otherwise report seven names, and the UI would offer two slots that do not exist.
	w.names = device.FitFilterNames(names, w.slots)
}

// Position returns the current 1-based slot, or 0 while the wheel is moving.
func (w *Wheel) Position() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return 0, device.ErrNotConnected
	}
	var pos int32
	if err := efwCheck("EFWGetPosition", w.sdk.getPosition(w.id, &pos)); err != nil {
		return 0, err
	}
	if pos == movingPosition {
		return 0, nil
	}
	return int(pos) + 1, nil // the SDK is 0-based; every UI here is 1-based
}

// SetPosition starts a move. It returns as soon as the wheel accepts the command — settling is
// WaitSettled's job, because a five-second blocking move would freeze the whole sequencer.
func (w *Wheel) SetPosition(slot int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return device.ErrNotConnected
	}
	if slot < 1 || slot > w.slots {
		return fmt.Errorf("efw: slot %d is outside this wheel's 1–%d", slot, w.slots)
	}
	return efwCheck("EFWSetPosition", w.sdk.setPosition(w.id, int32(slot-1)))
}

// WaitSettled blocks until the wheel stops moving.
//
// This is not optional politeness: EFWGetPosition returns -1 for the whole rotation, so without
// waiting, the next exposure would start with a filter edge across the sensor.
func (w *Wheel) WaitSettled(ctx context.Context) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		pos, err := w.Position()
		if err != nil {
			return err
		}
		if pos > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("efw: the wheel was still moving after 30 seconds")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// State describes the wheel to the UI.
func (w *Wheel) State() (device.WheelState, error) {
	pos, err := w.Position()
	w.mu.Lock()
	st := device.WheelState{
		Info:  device.Info{ID: fmt.Sprintf("efw-%d", w.id), Name: w.name, Driver: "efw", Kind: "wheel"},
		Slots: w.slots, Names: append([]string(nil), w.names...),
	}
	w.mu.Unlock()
	st.Position = pos
	st.Moving = pos == 0
	return st, err
}

// Calibrate re-finds the wheel's home position. Worth running after the wheel is bumped or a filter
// is changed — a wheel that has lost its home reports the wrong slot for every frame afterwards,
// and the FITS headers would then name the wrong filter.
func (w *Wheel) Calibrate(ctx context.Context) error {
	w.mu.Lock()
	if !w.connected {
		w.mu.Unlock()
		return device.ErrNotConnected
	}
	err := efwCheck("EFWCalibrate", w.sdk.calibrate(w.id))
	w.mu.Unlock()
	if err != nil {
		return err
	}
	// Calibration turns the wheel through every slot, so the same settle rule applies.
	return w.WaitSettled(ctx)
}

// efwCheck decodes a return code. The call name is included because every step of the connect
// sequence (GetID → Open → GetProperty) returns the same bare integer, and "SDK error 4" with no
// idea which call produced it is a dead end at the telescope.
func efwCheck(call string, code int32) error {
	if code == 0 {
		return nil
	}
	return fmt.Errorf("efw: %s failed: %s", call, efwErrorText(code))
}

// efwErrorText names the documented EFW error codes.
func efwErrorText(code int32) string {
	switch code {
	case 1:
		return "invalid index (no wheel at that position)"
	case 2:
		return "invalid id"
	case 3:
		return "invalid value"
	case 4:
		return "the wheel was removed, or is already open in another program — close ASIStudio/EFW " +
			"utilities and unplug/replug the wheel"
	case 5:
		return "the wheel is moving"
	case 6:
		return "the wheel is in an error state — power-cycle it"
	case 7:
		return "general error"
	case 8:
		return "not supported"
	case 9:
		return "the wheel is closed"
	default:
		return fmt.Sprintf("SDK error %d", code)
	}
}

func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
