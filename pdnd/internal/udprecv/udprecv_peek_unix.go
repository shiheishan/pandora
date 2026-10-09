//go:build unix

package udprecv

import (
	"syscall"
	"unsafe"
)

// peek 非阻塞地看 socket 里有没有包（1 字节 MSG_PEEK，不取走、不要来源地址，不分配）。
// 空返回 ErrWouldBlock；0 字节的数据报也算有包。
func peek(fd uintptr) error {
	var one [1]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_RECVFROM, fd, uintptr(unsafe.Pointer(&one[0])), 1, syscall.MSG_PEEK|syscall.MSG_DONTWAIT, 0, 0)
	if errno != 0 {
		if errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK {
			return ErrWouldBlock
		}
		return errno
	}
	return nil
}
