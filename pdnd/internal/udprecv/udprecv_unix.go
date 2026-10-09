//go:build unix && !linux

package udprecv

import "syscall"

// Supported：本平台能用 RawConn 就绪等待后收包（一次一包）。
const Supported = true

type batchSys struct{}

func (s *batchSys) init([][]byte) {}

// read 在 fd 上非阻塞地收一包（没有 recvmmsg 的平台）。
func (b *Batch) read(fd uintptr, size int) (int, error) {
	if size < 1 || len(b.Bufs) == 0 {
		return 0, nil
	}
	n, from, err := syscall.Recvfrom(int(fd), b.Bufs[0], syscall.MSG_DONTWAIT)
	if err != nil {
		if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
			return 0, ErrWouldBlock
		}
		return 0, err
	}
	b.N[0] = n
	switch sa := from.(type) {
	case *syscall.SockaddrInet4:
		b.From[0] = addrPort(syscall.AF_INET, uint16(sa.Port), sa.Addr, [16]byte{}, 0)
	case *syscall.SockaddrInet6:
		b.From[0] = addrPort(syscall.AF_INET6, uint16(sa.Port), [4]byte{}, sa.Addr, sa.ZoneId)
	}
	return 1, nil
}
