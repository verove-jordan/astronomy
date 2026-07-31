package nexstar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"go.bug.st/serial"
)

// The real serial link. Only the core go.bug.st/serial package is used — its `enumerator`
// sub-package needs cgo on macOS, and this whole engine is deliberately cgo-free.

// The two conditions the rest of the driver has to tell apart, named here because this is the only
// file that knows which library produced them. Everything above this line deals in these sentinels,
// so the recovery logic can be exercised by a test fake that never imports go.bug.st/serial.
var (
	// ErrLinkGone means the file descriptor is finished — the adapter was unplugged, the port
	// re-enumerated under a new name, or the port was closed underneath us. It is deliberately
	// distinct from "the mount is slow" and from "the reply was malformed": only this one is worth
	// reconnecting for.
	ErrLinkGone = errors.New("the serial link to the hand controller is gone")
	// ErrPortBusy means another program holds the port. macOS serial opens take TIOCEXCL, so the
	// second opener is refused outright — usually a second astrostack, CPWI, or a planetarium app
	// left connected. Saying "no mount found" here sends the user hunting for a hardware fault that
	// does not exist.
	ErrPortBusy = errors.New("the serial port is held by another program")
)

// openSerial opens a hand-controller port at the protocol's fixed 9600 8N1.
func openSerial(path string) (Port, error) {
	port, err := serial.Open(path, &serial.Mode{
		BaudRate: BaudRate,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return nil, translateSerialError(err)
	}
	if err := port.SetReadTimeout(replyTimeout); err != nil {
		_ = port.Close()
		return nil, err
	}
	return serialPort{port}, nil
}

// translateSerialError maps the library's (and the kernel's) errors onto this package's sentinels.
//
// Two traps are baked in here. The library returns *PortError, not PortError, and its methods hang
// off the value receiver — so `errors.As(err, &serial.PortError{})` compiles, reads correctly, and
// silently never matches. And not every failure arrives as a PortError at all: a write to a
// vanished adapter surfaces as a bare syscall.EIO and reopening its path as ENOENT, because by then
// the device node itself is gone.
func translateSerialError(err error) error {
	if err == nil {
		return nil
	}
	var pe *serial.PortError
	if errors.As(err, &pe) {
		switch pe.Code() {
		case serial.PortBusy:
			return fmt.Errorf("%w: %v", ErrPortBusy, err)
		case serial.PortClosed, serial.PortNotFound:
			return fmt.Errorf("%w: %v", ErrLinkGone, err)
		case serial.PermissionDenied:
			return fmt.Errorf("%w: %v", os.ErrPermission, err)
		}
		return err
	}
	switch {
	case errors.Is(err, syscall.EBUSY):
		return fmt.Errorf("%w: %v", ErrPortBusy, err)
	case errors.Is(err, syscall.EACCES):
		return fmt.Errorf("%w: %v", os.ErrPermission, err)
	case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENXIO),
		errors.Is(err, syscall.EIO), errors.Is(err, syscall.EBADF):
		return fmt.Errorf("%w: %v", ErrLinkGone, err)
	}
	return err
}

// serialPort adapts the library's port to the narrow Port interface, translating on the way so no
// library error type escapes this file.
type serialPort struct{ serial.Port }

func (s serialPort) SetReadTimeout(d time.Duration) error { return s.Port.SetReadTimeout(d) }

func (s serialPort) Read(p []byte) (int, error) {
	n, err := s.Port.Read(p)
	return n, translateSerialError(err)
}

func (s serialPort) Write(p []byte) (int, error) {
	n, err := s.Port.Write(p)
	return n, translateSerialError(err)
}

// PortInfo is one candidate serial device offered to the UI.
type PortInfo struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	// Likely marks ports that look like a USB-serial adapter — the NexStar+ hand controller carries
	// a Prolific chip, so it appears as /dev/cu.usbserial-*. Bluetooth and debug ports never do.
	Likely bool `json:"likely"`
}

// ListPorts enumerates the serial devices worth offering. On macOS it deliberately reports the
// call-out (cu.*) devices: opening the matching tty.* device blocks waiting for carrier detect,
// which simply hangs.
func ListPorts() []PortInfo {
	names, err := serial.GetPortsList()
	if err != nil {
		return nil
	}
	out := make([]PortInfo, 0, len(names))
	for _, name := range names {
		base := filepath.Base(name)
		if strings.HasPrefix(base, "tty.") && hasCallout(names, base) {
			continue // prefer the cu.* twin
		}
		if isNoise(base) {
			continue
		}
		out = append(out, PortInfo{Path: name, Label: base, Likely: looksLikeAdapter(base)})
	}
	return out
}

// hasCallout reports whether a tty.* device has a cu.* twin in the list.
func hasCallout(names []string, ttyBase string) bool {
	want := "cu." + strings.TrimPrefix(ttyBase, "tty.")
	for _, n := range names {
		if filepath.Base(n) == want {
			return true
		}
	}
	return false
}

// isNoise filters the ports that are never a telescope: Bluetooth channels, the kernel debug
// console, and macOS's wireless-iAP endpoints.
func isNoise(base string) bool {
	lower := strings.ToLower(base)
	for _, bad := range []string{"bluetooth", "debug-console", "wlan-debug", "incoming-port"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

// looksLikeAdapter matches the USB-serial naming the NexStar+ hand controller produces.
func looksLikeAdapter(base string) bool {
	lower := strings.ToLower(base)
	for _, good := range []string{"usbserial", "usbmodem", "slab", "ftdi", "ttyusb", "ttyacm", "wchusb"} {
		if strings.Contains(lower, good) {
			return true
		}
	}
	return false
}

// Probe reports whether this build can talk to a mount at all, and what it can see. Used for the
// device server's driver report, so "no mount" reads as a state rather than a failure.
func Probe() (string, error) {
	ports := ListPorts()
	if len(ports) == 0 {
		return "", fmt.Errorf("no serial ports found — connect the hand controller's USB cable")
	}
	// A Bluetooth speaker is a serial port too. Reporting the driver as "available" because SOMETHING
	// is listed would be a lie the user only discovers when connecting fails, so availability means
	// at least one port that could plausibly be a hand controller.
	likely := make([]string, 0, len(ports))
	others := make([]string, 0, len(ports))
	for _, p := range ports {
		if p.Likely {
			likely = append(likely, p.Label)
		} else {
			others = append(others, p.Label)
		}
	}
	if len(likely) == 0 {
		return "", fmt.Errorf(
			"no USB-serial adapter found (saw %s) — connect the hand controller's USB cable",
			strings.Join(others, ", "))
	}
	return "hand controller candidates: " + strings.Join(likely, ", "), nil
}

// DefaultPort guesses the hand controller's port: the first that looks like a USB-serial adapter.
// A guess only — the UI still lets the user choose, because a second adapter would be ambiguous.
func DefaultPort() string {
	for _, p := range ListPorts() {
		if p.Likely {
			return p.Path
		}
	}
	return ""
}
