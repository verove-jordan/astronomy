//go:build !unix

package localfs

// diskUsage is unavailable on non-unix platforms; capacity is simply omitted from the drive listing.
func diskUsage(string) (total, free uint64) { return 0, 0 }
