package nexstar

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.bug.st/serial"
)

// Port naming and error translation — the two pieces of serial.go that decide what the user is told
// when there is no mount. Both were entirely untested.

func TestLooksLikeAdapter_MatchesTheNamesAHandControllerProduces(t *testing.T) {
	tests := []struct {
		base string
		want bool
	}{
		{"cu.usbserial-1420", true}, // the Prolific bridge in a NexStar+
		{"cu.PL2303G-usbserial", true},
		{"cu.usbmodem14201", true},  // a CDC device, e.g. some StarSense revisions
		{"cu.SLAB_USBtoUART", true}, // Silicon Labs
		{"cu.usbserial-A50285BI", true},
		{"ttyUSB0", true}, // Linux, under `just stack`
		{"ttyACM0", true}, // Linux CDC
		{"cu.wchusbserial", true},
		{"cu.Bluetooth-Incoming-Port", false},
		{"cu.BoseFlexSoundLink", false},
		{"cu.debug-console", false},
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeAdapter(tt.base))
		})
	}
}

func TestIsNoise_DropsThePortsThatAreNeverATelescope(t *testing.T) {
	tests := []struct {
		base string
		want bool
	}{
		{"cu.Bluetooth-Incoming-Port", true},
		{"cu.debug-console", true},
		{"cu.wlan-debug", true},
		{"cu.usbserial-1420", false},
		{"cu.BoseFlexSoundLink", false}, // a paired speaker: real, just not likely
	}
	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			assert.Equal(t, tt.want, isNoise(tt.base))
		})
	}
}

func TestHasCallout_PrefersTheCallOutTwin(t *testing.T) {
	names := []string{"/dev/cu.usbserial-1420", "/dev/tty.usbserial-1420", "/dev/tty.lonely"}
	// Opening the tty.* device blocks waiting for carrier detect, which on a hand controller simply
	// hangs — so it is dropped whenever its cu.* twin exists.
	assert.True(t, hasCallout(names, "tty.usbserial-1420"))
	assert.False(t, hasCallout(names, "tty.lonely"))
}

func TestTranslateSerialError_NamesTheConditionsRecoveryBranchesOn(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"another program holds it", &serial.PortError{}, ErrPortBusy, true}, // zero code is PortBusy
		{"raw EBUSY from the kernel", fmt.Errorf("open: %w", syscall.EBUSY), ErrPortBusy, true},
		{"the node is gone after a re-enumeration", fmt.Errorf("open: %w", syscall.ENOENT), ErrLinkGone, true},
		{"a write to a vanished adapter", fmt.Errorf("write: %w", syscall.EIO), ErrLinkGone, true},
		{"the descriptor is closed", fmt.Errorf("read: %w", syscall.EBADF), ErrLinkGone, true},
		{"no permission", fmt.Errorf("open: %w", syscall.EACCES), os.ErrPermission, true},
		{"something else entirely", errors.New("boom"), ErrLinkGone, false},
		{"no error at all", nil, ErrLinkGone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translateSerialError(tt.err)
			assert.Equal(t, tt.want, errors.Is(got, tt.target))
		})
	}
}

func TestTranslateSerialError_MatchesThePointerTypeTheLibraryActuallyReturns(t *testing.T) {
	// The library's methods hang off the VALUE receiver but it returns *PortError, so
	// `errors.As(err, &serial.PortError{})` compiles, reads correctly and silently never matches.
	// Getting this wrong would make every unplug look like an ordinary error and disable reconnection.
	var pe *serial.PortError
	assert.True(t, errors.As(&serial.PortError{}, &pe))

	closed := serial.PortError{}
	assert.NotErrorIs(t, translateSerialError(closed), ErrPortBusy,
		"a PortError passed by value is not what the library produces and must not be relied on")
}

func TestListPorts_ReportsRealDevicesWithoutCrashing(t *testing.T) {
	// Whatever is attached to the machine running this, the listing must be well formed: a Bluetooth
	// channel is a serial port too, and offering one as a telescope is how a connect attempt hangs.
	for _, p := range ListPorts() {
		assert.NotEmpty(t, p.Path)
		assert.NotEmpty(t, p.Label)
		assert.False(t, isNoise(p.Label))
		assert.Equal(t, looksLikeAdapter(p.Label), p.Likely)
	}
}
