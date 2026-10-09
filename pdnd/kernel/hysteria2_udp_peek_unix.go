//go:build unix

package kernel

import (
	"errors"
	"syscall"
)

// 下行等包用 MSG_PEEK（只窥视、不取走），收包用 MSG_DONTWAIT（没有就立即返回），
// 见 hysteria2_udp_relay.go 的 hy2DownlinkUDPPeek。
const (
	hy2UDPPeekSupported = true
	hy2UDPPeekFlag      = syscall.MSG_PEEK
	hy2UDPDontWaitFlag  = syscall.MSG_DONTWAIT
)

// isHy2UDPWouldBlock 判断一次非阻塞收包是不是「队列已空」。
func isHy2UDPWouldBlock(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}
