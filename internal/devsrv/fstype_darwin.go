//go:build darwin

package devsrv

import "syscall"

// filesystemType names the filesystem holding path ("ntfs", "exfat", "apfs", …), or "" if unknown.
//
// It exists to turn a dead end into a next step. "read-only file system" on an external drive is
// almost always macOS mounting NTFS, which it can read but never write; without naming that, the
// message sends someone hunting for a permissions or Docker problem that does not exist.
func filesystemType(path string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return ""
	}
	b := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
