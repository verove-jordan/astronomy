//go:build !darwin

package devsrv

// filesystemType is darwin-only; elsewhere the read-only message stands on its own.
func filesystemType(string) string { return "" }
