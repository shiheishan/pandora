package kernel

import (
	"errors"
	"net"
	"syscall"

	"golang.org/x/net/ipv4"
)

// 上行 UDP 的 GSO（UDP_SEGMENT）合包。
//
// QUIC 类入站的上行是「客户端 → 节点 → 同一个目标」的一长串同长包（iperf、
// 视频通话、QUIC-in-QUIC）。sendmmsg 只省了系统调用次数，内核里每个包仍要各自
// 走一遍 UDP/IP/路由/netfilter 和网卡驱动；节点验收的 profile 里上游发包占 pdnd
// CPU 的两成。GSO 把同一目标、同样长度的连续包交给内核当成一个大包处理，到
// 出口网卡（或驱动前的软件分段）才切开，线上的包与逐包发送逐字节相同。
//
// 合包规则（一条 GSO 消息）：同一目标、段长 = 第一个包的长度、后面的包都等长，
// 只有最后一个可以更短；段长不超过 hy2GSOMaxSegment（以太网 MTU 内，超出会被
// 内核以 EINVAL 拒绝），总长不超过 hy2GSOMaxBytes。负载不复制：消息的 iovec
// 直接指向各个包。
//
// 私网拦截照旧有效：把关层（outbound 的带检查批量接口）按消息的目标判定，一条
// GSO 消息里的包目标相同，被拦即整条丢弃、照常计为已发出，与逐包同语义。
//
// 内核不认 UDP_SEGMENT（4.18 以前）、出口不支持（无校验和卸载、IPsec）或段长
// 超过路径 MTU 时，sendmmsg 对这条消息报 EIO / EINVAL 等：本会话关掉 GSO，
// 从这条起逐条重发，不丢包。

const (
	// hy2GSOMaxSegment 是参与合包的最大段长：1500 字节以太网 MTU 减 IPv4 与
	// UDP 头。更长的包（会被 IP 分片）照旧单发。
	hy2GSOMaxSegment = 1472
	// hy2GSOMaxBytes 是一条 GSO 消息的负载上限，留在 64KB IP 包之内。
	hy2GSOMaxBytes = 65000
)

// buildMessages 把 payloads[from:] 编成 WriteBatch 的消息：开着 GSO 时同目标的
// 连续等长包合成一条，否则一包一条。
func (w *hy2BatchWriter) buildMessages(from int) {
	w.messages = w.messages[:0]
	w.first = w.first[:0]
	for i := from; i < len(w.payloads); {
		end := i + 1
		if w.gso {
			end = w.gsoRunEnd(i)
		}
		message := ipv4.Message{Addr: w.addrs[i]}
		if end-i == 1 {
			w.buffers[i][0] = w.payloads[i]
			message.Buffers = w.buffers[i][:]
		} else {
			message.Buffers = w.payloads[i:end]
			slot := len(w.messages) * udpSegmentCmsgSpace
			message.OOB = putUDPSegmentCmsg(w.oob[slot:slot+udpSegmentCmsgSpace], uint16(len(w.payloads[i])))
		}
		w.messages = append(w.messages, message)
		w.first = append(w.first, i)
		i = end
	}
}

// gsoRunEnd 返回从 payloads[i] 起能合进一条 GSO 消息的包的结束下标（不含）。
func (w *hy2BatchWriter) gsoRunEnd(i int) int {
	segment := len(w.payloads[i])
	if segment == 0 || segment > hy2GSOMaxSegment {
		return i + 1
	}
	total, end := segment, i+1
	for end < len(w.payloads) {
		size := len(w.payloads[end])
		if size == 0 || size > segment || total+size > hy2GSOMaxBytes || !sameUDPAddr(w.addrs[end], w.addrs[i]) {
			break
		}
		total += size
		end++
		if size < segment {
			// 更短的只能是最后一段。
			break
		}
	}
	return end
}

// sameUDPAddr 比较两个目标；解析缓存命中时是同一个指针。
func sameUDPAddr(a, b *net.UDPAddr) bool {
	return a == b || (a.Port == b.Port && a.IP.Equal(b.IP) && a.Zone == b.Zone)
}

// isUDPGSOError 判断一次批量发送的失败是不是 GSO 本身不被支持或不被接受。
func isUDPGSOError(err error) bool {
	return errors.Is(err, syscall.EIO) || errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOPROTOOPT) || errors.Is(err, syscall.EOPNOTSUPP)
}
