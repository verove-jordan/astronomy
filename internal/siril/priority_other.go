//go:build !unix

package siril

// setPriority is a no-op on platforms without POSIX setpriority (the engine targets a macOS host).
func setPriority(pid, nice int) {}
