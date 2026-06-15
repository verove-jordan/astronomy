//go:build !windows

package gimp

import "syscall"

// detachAttr puts the spawned GIMP in its own session so it survives the engine process.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
