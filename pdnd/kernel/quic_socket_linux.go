//go:build linux

package kernel

import (
	"log/slog"
	"net"
	"syscall"
)

// warnSmallQUICSocketBuffers 在 hy2 / TUIC 入站起来后检查 quic-go 实际拿到的 UDP
// 缓冲，太小就告警一次。
//
// quic-go 自己会把缓冲往 8MB 调，但受 net.core.rmem_max / wmem_max 限制；pdnd 以
// 普通用户运行、没有 CAP_NET_ADMIN，绕不过上限（SO_RCVBUFFORCE 被拒），而且
// sagernet/quic-go 调不上去时不报任何告警。发行版缺省上限约 208KB，单条连接几百
// Mbps 时一个调度停顿就能把缓冲灌满、整批丢包（客户端看到的就是限速与重传）。
// Linux 读回来的值是设置值的两倍，所以按「不到期望的一半」判。
//
// UDP 转发的出站 socket（hysteria2_udp.go）按同一口径申请、受同一上限，拿不到时
// 不逐会话告警：这条告警已经说明了原因与办法，每个入站只出一次。
//
// 走包级 slog（main 已把进程日志设成默认 logger），与其他日志同为 level= 结构化格式。
func warnSmallQUICSocketBuffers(protocol string, port int, conn net.PacketConn) {
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return
	}
	raw, err := udp.SyscallConn()
	if err != nil {
		return
	}
	var rcv, snd int
	_ = raw.Control(func(fd uintptr) {
		rcv, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
		snd, _ = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if rcv >= quicSocketBufferWant/2 && snd >= quicSocketBufferWant/2 {
		return
	}
	slog.Warn("QUIC 入站的 UDP 缓冲偏小（UDP 转发的出站 socket 受同一上限），单连接大流量会丢包；请把 sysctl net.core.rmem_max 与 net.core.wmem_max 设到 8388608 以上后重启 pdnd（面板的节点安装脚本会写 /etc/sysctl.d/90-pandora-native.conf，手工安装见 release/README.md）",
		"protocol", protocol, "port", port, "接收缓冲KB", rcv/1024, "发送缓冲KB", snd/1024)
}
