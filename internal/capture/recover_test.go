package capture

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Which failures are worth waiting out. Getting this wrong in either direction costs a night: too
// narrow and a nudged cable ends the session, too broad and a read-only output directory keeps the
// session "running" until dawn while writing nothing.

func TestIsRecoverable_SeparatesHardwareFromEverythingElse(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the device server says nothing is connected",
			err:  errors.New(`device error: {"error":"not_connected"}`),
			want: true,
		},
		{
			name: "the driver is unavailable",
			err:  errors.New(`503: {"error":"driver_unavailable","detail":"no serial port selected"}`),
			want: true,
		},
		{
			name: "the adapter was unplugged",
			err:  fmt.Errorf("read reply: %s", "the serial link to the hand controller is gone"),
			want: true,
		},
		{
			name: "another program took the port",
			err:  errors.New("reopen /dev/cu.usbserial-1420: the serial port is held by another program"),
			want: true,
		},
		{
			name: "the device server itself restarted",
			err:  errors.New(`Post "http://127.0.0.1:8084/camera/expose": dial tcp: connection refused`),
			want: true,
		},
		{
			name: "the output directory is read-only",
			err:  errors.New("write frame: open /out/L_0001.fit: permission denied"),
			want: false,
		},
		{
			name: "the disk is full",
			err:  errors.New("write frame: no space left on device"),
			want: false,
		},
		{
			name: "a bad request the caller must fix",
			err:  errors.New("400: exposure must be positive"),
			want: false,
		},
		{
			name: "no error at all",
			err:  nil,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRecoverable(tt.err))
		})
	}
}

func TestIsRecoverable_IgnoresCase(t *testing.T) {
	// The hints are matched against whatever the device server actually wrote, and its wording is not
	// guaranteed to keep its capitalisation across refactors.
	assert.True(t, isRecoverable(errors.New("Device Error: NOT_CONNECTED")))
}

// The end-to-end contract this file exists to protect, and the one that was broken.
//
// A wheel or camera that goes away mid-run must be waited out, not treated as the end of the night.
// It only is if the whole chain lines up: the ZWO driver wraps the SDK's "removed" code as
// device.ErrNotConnected, devsrv maps that sentinel to code "not_connected", capture.Error renders
// the code into the message, and isRecoverable matches it. Every link but the first was already
// there — so a real EFWGetPosition error 4 arrived as a bare 500, and session 10 died at frame 5 of
// 80 on a wheel that was healthy again minutes later.
func TestIsRecoverable_AWheelThatWentAwayIsWaitedOut(t *testing.T) {
	// Exactly what Client.do builds from the device server's 409 reply.
	removed := &Error{
		Status: 409,
		Code:   "not_connected",
		Message: `efw: EFWGetPosition failed: the wheel was removed, or is already open in ` +
			`another program — close ASIStudio/EFW utilities and unplug/replug the wheel`,
	}
	wrapped := fmt.Errorf(`select filter %q (slot 2): %w`, "R", removed)
	assert.True(t, isRecoverable(wrapped),
		"a wheel that went away must pause the session, not end it")

	// The same failure BEFORE the driver named it: no code, so nothing could recognise it. Kept as a
	// test so the regression is visible rather than implied.
	uncoded := &Error{Status: 500, Message: removed.Message}
	assert.False(t, isRecoverable(fmt.Errorf(`select filter %q (slot 2): %w`, "R", uncoded)),
		"this is the shape that ended the night; it must stay distinguishable")
}

// A camera that accepts an exposure and never finishes it is the same kind of trouble as one that
// vanished: usually the USB bus renegotiating under a rig that comes straight back. It ended the
// night instead, because the poll loop's own error carried no sentinel and the hints only match what
// the DEVICE SERVER wrote. MEASURED: session 12 died at 40 of 60 on "exposure did not complete
// within 3m0s", on the same hub whose serial adapter had just dropped off.
func TestIsRecoverable_AStalledExposureIsWaitedOut(t *testing.T) {
	stalled := fmt.Errorf("%w within %s", errExposureStalled, 3*time.Minute)

	assert.True(t, isRecoverable(stalled))
	assert.Contains(t, stalled.Error(), "3m0s", "the limit stays in the message")

	// The distinction that has to survive: a camera that reports a FAILED exposure has answered, and
	// so has a full disk. Neither is waited out.
	assert.False(t, isRecoverable(errors.New("the camera reported a failed exposure")))
	assert.False(t, isRecoverable(errors.New("write frame: no space left on device")))
}
