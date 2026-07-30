package asi

import (
	"fmt"
	"runtime"
	"strings"
)

// Turning a dlopen failure into advice that actually works.
//
// The failure a user of this project will hit is specific and predictable: ZWO publish NO arm64
// macOS library. Their SDK, and their own ASIStudio, are i386/x86_64 only — verified with `lipo`
// against the bundled libASICamera2.dylib. So a native arm64 engine can never load it, and no
// amount of downloading a newer SDK will change that.
//
// The fix is not a different download, it is a different process architecture: build the device
// sidecar as x86_64 and let Rosetta run it. That is exactly why device I/O lives in its own
// process, and it is verified working on this hardware. The raw dynamic-loader error says
// "missing compatible architecture" in 400 characters of paths; this says what to do instead.
func explainLoadFailure(path string, err error) error {
	if isArchMismatch(err) {
		return fmt.Errorf(
			"asi: %s has no %s build — ZWO publish no arm64 macOS library (their own ASIStudio is "+
				"x86_64 too). Run the device server under Rosetta instead: `just device-x86`",
			path, runtime.GOARCH)
	}
	return fmt.Errorf("asi: cannot load %s: %w", path, err)
}

// isArchMismatch recognises the loader's way of saying "wrong CPU", on both macOS and Linux.
func isArchMismatch(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"missing compatible architecture", // macOS, fat binary without our slice
		"incompatible architecture",       // macOS, thin binary of the wrong arch
		"wrong ELF class",                 // Linux, 32/64-bit mismatch
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
