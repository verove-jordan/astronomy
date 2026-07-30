package efw

import (
	"fmt"
	"runtime"
	"strings"
)

// explainLoadFailure gives the same advice as the camera driver: the EFW library ships in the same
// Intel-only ASIStudio package, so it fails identically on Apple Silicon and has the same fix —
// run the device sidecar under Rosetta rather than hunting for an arm64 build that does not exist.
func explainLoadFailure(path string, err error) error {
	if isArchMismatch(err) {
		return fmt.Errorf(
			"efw: %s has no %s build — ZWO publish no arm64 macOS library. Run the device server "+
				"under Rosetta instead: `just device-x86`",
			path, runtime.GOARCH)
	}
	return fmt.Errorf("efw: cannot load %s: %w", path, err)
}

func isArchMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"missing compatible architecture",
		"incompatible architecture",
		"wrong ELF class",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
