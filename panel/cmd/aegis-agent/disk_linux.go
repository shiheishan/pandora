//go:build linux

package main

import "syscall"

// diskUsage returns filesystem capacity visible to an unprivileged process.
// Bavail deliberately excludes blocks reserved for root.
func diskUsage(path string) (totalGB, usedGB int) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	total := uint64(st.Blocks) * bs
	avail := uint64(st.Bavail) * bs
	const gb = 1 << 30
	return int(total / gb), int((total - avail) / gb)
}
