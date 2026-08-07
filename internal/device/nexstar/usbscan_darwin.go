package nexstar

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Reading the USB bus on macOS, without cgo.
//
// The obvious API for this is IOKit, which means cgo — forbidden here for the same reason the
// serial library's `enumerator` sub-package is unused (serial.go). `ioreg` is the same registry
// rendered as text, so shelling out to it keeps the engine cgo-free and costs one process.
//
// Two variants were tried before this one. `system_profiler SPUSBDataType` returns an EMPTY device
// list on macOS 26 even with devices attached, so it cannot be relied on. `ioreg -p IOUSB` walks
// the USB plane, which does not contain IOSerialBSDClient nodes at all — so the /dev/cu.* path can
// never be attributed to the chip that owns it. The IOService plane rooted at each USB device is
// the only rendering that carries both the descriptor and the serial device node.

// usbScanTimeout bounds the ioreg call. It normally returns in well under a second; a diagnosis
// that hangs is worse than one that says it could not read the bus.
const usbScanTimeout = 10 * time.Second

func scanUSB(ctx context.Context) ([]USBDevice, error) {
	ctx, cancel := context.WithTimeout(ctx, usbScanTimeout)
	defer cancel()

	// -r roots the listing at each matching object and includes its subtree, -l prints properties,
	// -w0 disables line truncation (without it long property lines are cut mid-value).
	cmd := exec.CommandContext(ctx, "ioreg", "-r", "-c", "IOUSBHostDevice", "-l", "-w0")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ioreg: %w", err)
	}
	return parseIoreg(string(out)), nil
}
