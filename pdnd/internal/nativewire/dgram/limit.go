// Package dgram 是 Hysteria2 与 TUIC 两个 fork 共用的 QUIC DATAGRAM 尺寸跟踪
// （Pandora 新增，不是上游代码）。
package dgram

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
)

// 上游 sing-quic 把 UDP 消息的分片上限写死成 1197 字节：1200 字节的用户包总要拆
// 成两片，QUIC 包数、系统调用与 ACK 都随之翻倍。quic-go 的真实上限随 PMTU 探测
// 从约 1240 涨到约 1415（以太网），官方 hysteria / tuic 服务端都按连接的实际上限
// 分片。这里改成按实际上限：
//
//   - 上限向 quic-go 问：SendDatagram 先比长度、超长立刻返回 DatagramTooLargeError
//     （带当前上限），不排队、不发包，所以拿一个必然超长的缓冲去问没有副作用；
//     结果缓存 RefreshInterval，跟上 PMTU 变化。
//   - 实际只用到上限减 AckMargin：quic-go 的上限估算没给同包的 ACK 帧留位置，
//     贴着上限的 DATAGRAM 遇到带 ACK 的包装不下，要等不带 ACK 的包，连续 10 次
//     装不下就被丢弃（packet_packer.go 的 DatagramFrameMaxPeekTimes）。上下行
//     同时有流量时几乎每个包都带 ACK，不留余量就会丢包。
//   - 问不到（连接不支持 DATAGRAM、已关闭）时退回上游的 1197。

// RefreshInterval 是缓存的上限多久向 quic-go 重新问一次。
const RefreshInterval = time.Second

// AckMargin 是在 quic-go 报的上限之外给同包 ACK 帧留的字节数：对端连接 ID 8
// 字节（quinn）、包号 4 字节时，仍能容下约 70 字节的 ACK 帧。
const AckMargin = 64

// Fallback 是问不到上限时的单条上限（上游 sing-quic 的 1200 - 3）。
const Fallback = 1200 - 3

// probe 必然超过任何 DATAGRAM 上限（帧上限 16383，且受 MTU 限制），只用来问上限。
var probe [1 << 16]byte

// Limit 缓存一条 QUIC 连接上单条 UDP 消息（含消息头）整包能发的字节上限。
// 零值可用。
type Limit struct {
	size    atomic.Int64 // 已扣 AckMargin 的上限，0 = 未知
	checkAt atomic.Int64 // UnixNano，到这之后重新问
}

// Size 返回单条消息（含消息头）整包能发的字节上限；分片时也按它切。
func (l *Limit) Size(conn *quic.Conn) int {
	if size := l.size.Load(); size != 0 && time.Now().UnixNano() < l.checkAt.Load() {
		return int(size)
	}
	return l.Refresh(conn)
}

// Refresh 立即向 quic-go 重新问上限（发送时遇到 DatagramTooLargeError，说明
// PMTU 变小了）。
func (l *Limit) Refresh(conn *quic.Conn) int {
	var tooLarge *quic.DatagramTooLargeError
	if err := conn.SendDatagram(probe[:]); !errors.As(err, &tooLarge) {
		return Fallback
	}
	// 对端声明了异常小的 max_datagram_frame_size 时可能扣成非正数：记成 1，
	// 由分片处拒绝（装不下消息头）。
	size := max(tooLarge.MaxDatagramPayloadSize-AckMargin, 1)
	l.size.Store(size)
	l.checkAt.Store(time.Now().Add(RefreshInterval).UnixNano())
	return int(size)
}
