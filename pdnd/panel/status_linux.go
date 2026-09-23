//go:build linux

package panel

import "syscall"

// diskBytes 返回根文件系统的容量与已用量。
//
// 用 Bavail 而不是 Bfree：前者是非特权进程真正能用的，后者含了给 root
// 预留的那部分（ext4 默认留 5%）。按 Bfree 算，一块刚格式化的盘就会显示
// 已用 5%，运维看着会以为有东西占着。
func diskBytes(path string) (total, used uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	total = st.Blocks * bs
	avail := st.Bavail * bs
	if avail <= total {
		used = total - avail
	}
	return total, used
}
