//go:build !linux

package kernel

import "net"

// 非 Linux 不检查 UDP 缓冲上限（节点只在 Linux 上部署）。
func warnSmallQUICSocketBuffers(string, int, net.PacketConn) {}
