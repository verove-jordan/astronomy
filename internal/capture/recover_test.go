package capture

import (
	"errors"
	"fmt"
	"testing"

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
