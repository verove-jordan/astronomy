//go:build !linux && !darwin

package transient

// platformBudget has no probe on this platform — MemBudget falls back to its default.
func platformBudget() int64 { return 0 }
