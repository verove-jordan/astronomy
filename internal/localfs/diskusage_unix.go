//go:build unix

package localfs

import "syscall"

// diskUsage returns the total and free bytes of the filesystem holding path, or (0, 0) on any error. The
// Statfs_t block-size field is int64 on Linux and uint32 on Darwin, so it is widened via uint64 for both.
func diskUsage(path string) (total, free uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize
}
