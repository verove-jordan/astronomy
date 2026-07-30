package transient

import "golang.org/x/sys/unix"

// platformBudget is half the physical memory: the host engine has the machine largely to itself
// (Siril/GIMP peaks are sequential with this pass), and macOS reports no MemAvailable equivalent.
func platformBudget() int64 {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return 0
	}
	return int64(total / 2)
}
