//go:build linux

package udprecv

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

// Supported：本平台能用 RawConn 就绪等待后批量收包。
const Supported = true

// mmsghdr 对应 linux/socket.h 的 struct mmsghdr（Go 的自然对齐与 C 一致：64 位上 64 字节）。
type mmsghdr struct {
	hdr syscall.Msghdr
	len uint32
}

type batchSys struct {
	hdrs  []mmsghdr
	iovs  []syscall.Iovec
	names []syscall.RawSockaddrInet6
}

func (s *batchSys) init(bufs [][]byte) {
	s.hdrs = make([]mmsghdr, len(bufs))
	s.iovs = make([]syscall.Iovec, len(bufs))
	s.names = make([]syscall.RawSockaddrInet6, len(bufs))
	for i, buf := range bufs {
		s.iovs[i].Base = &buf[0]
		s.iovs[i].SetLen(len(buf))
		s.hdrs[i].hdr.Iov = &s.iovs[i]
		s.hdrs[i].hdr.Iovlen = 1
	}
}

// read 在 fd 上非阻塞地收至多 size 条（recvmmsg）。
func (b *Batch) read(fd uintptr, size int) (int, error) {
	size = min(size, len(b.Bufs))
	for i := range size {
		h := &b.sys.hdrs[i].hdr
		h.Name = (*byte)(unsafe.Pointer(&b.sys.names[i]))
		h.Namelen = syscall.SizeofSockaddrInet6
		h.Control = nil
		h.SetControllen(0)
		h.Flags = 0
	}
	r, _, errno := syscall.Syscall6(syscall.SYS_RECVMMSG, fd, uintptr(unsafe.Pointer(&b.sys.hdrs[0])), uintptr(size), syscall.MSG_DONTWAIT, 0, 0)
	if errno != 0 {
		if errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK {
			return 0, ErrWouldBlock
		}
		return 0, errno
	}
	n := int(r)
	for i := range n {
		b.N[i] = int(b.sys.hdrs[i].len)
		name := &b.sys.names[i]
		port := binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&name.Port))[:])
		var addr4 [4]byte
		if name.Family == syscall.AF_INET {
			sa4 := (*syscall.RawSockaddrInet4)(unsafe.Pointer(name))
			addr4 = sa4.Addr
		}
		b.From[i] = addrPort(name.Family, port, addr4, name.Addr, name.Scope_id)
	}
	return n, nil
}
