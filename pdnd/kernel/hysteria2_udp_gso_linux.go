//go:build linux

package kernel

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

// hy2UDPGSOSupported：UDP_SEGMENT 只在 Linux 上有；内核太老时第一次发送失败
// 即按会话关掉（见 hysteria2_udp_gso.go）。
const hy2UDPGSOSupported = true

// udpSegmentOption 是 linux/udp.h 的 UDP_SEGMENT（层级 SOL_UDP = IPPROTO_UDP）。
const udpSegmentOption = 103

var udpSegmentCmsgSpace = syscall.CmsgSpace(2)

// putUDPSegmentCmsg 在 b 里写一条 UDP_SEGMENT 控制信息（段长 size），返回它。
func putUDPSegmentCmsg(b []byte, size uint16) []byte {
	b = b[:udpSegmentCmsgSpace]
	clear(b)
	header := (*syscall.Cmsghdr)(unsafe.Pointer(&b[0]))
	header.Level = syscall.IPPROTO_UDP
	header.Type = udpSegmentOption
	header.SetLen(syscall.CmsgLen(2))
	binary.NativeEndian.PutUint16(b[syscall.CmsgLen(0):], size)
	return b
}
