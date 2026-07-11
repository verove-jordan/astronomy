// Package buildinfo identifies the engine build that produced a run. Runs from a stale Docker
// engine looked like "the fix didn't work" — stamping the build into /api/health and every
// run record makes "which code produced this image" answerable at a glance.
package buildinfo

// Version and BuiltAt are injected at build time via
// -ldflags "-X .../internal/buildinfo.Version=$(git describe --tags --always --dirty) -X .../internal/buildinfo.BuiltAt=...".
// "dev" means an un-stamped `go run` / test binary.
var (
	Version = "dev"
	BuiltAt = ""
)

// String renders the build identity for logs and the version command.
func String() string {
	if BuiltAt == "" {
		return Version
	}
	return Version + " (" + BuiltAt + ")"
}
