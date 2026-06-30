//go:build unix

package siril

import "syscall"

// setPriority lowers the scheduling priority of pid by nice points so a full-throttle Siril stack
// still yields CPU to the rest of the system under contention (and costs nothing when it's idle).
// A non-positive nice leaves the priority unchanged. Best-effort: a failure is non-fatal.
func setPriority(pid, nice int) {
	if nice <= 0 {
		return
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice)
}
