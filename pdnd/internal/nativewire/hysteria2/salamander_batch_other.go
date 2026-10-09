//go:build !linux

package hysteria2

import "net"

// 非 Linux 没有 recvmmsg / GSO，服务端沿用上游的逐包混淆。
func newServerSalamanderConn(conn net.PacketConn, password []byte) net.PacketConn {
	return NewSalamanderConn(conn, password)
}
