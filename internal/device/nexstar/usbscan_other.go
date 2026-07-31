//go:build !darwin

package nexstar

import "context"

// Everywhere but macOS the diagnosis runs on the serial port list alone.
//
// The engine also runs fully containerised on Linux (`just stack`), where a hand controller would be
// passed through as /dev/ttyUSB0 by a kernel driver that either claimed the chip or did not — there
// is no "device present but unclaimed" state to explain, which is the only thing the USB scan is
// for. Returning a named error rather than an empty list keeps that distinction honest: the
// diagnosis reports "the bus could not be read", never "nothing is plugged in".
func scanUSB(context.Context) ([]USBDevice, error) { return nil, errUSBScanUnsupported }
