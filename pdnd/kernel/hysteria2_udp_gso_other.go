//go:build !linux

package kernel

// 非 Linux 没有 UDP_SEGMENT，上行逐条批量（或逐包）发送。
const hy2UDPGSOSupported = false

var udpSegmentCmsgSpace = 0

func putUDPSegmentCmsg(b []byte, _ uint16) []byte { return b[:0] }
