// Package udpmask 给 UDP 数据包套一层掩码。
//
// 它和上面跑什么协议无关——包装的是 net.PacketConn，mKCP、QUIC、原始
// UDP 都能往上套。上游把这一层叫 finalmask，从各传输里抽出来单独放，
// 我们跟随这个划分。
//
// # 为什么需要
//
// 裸 mKCP 的包在网上是可识别的：没有魔数也没有加密，但字段布局固定，
// 会话号在同一条连接里不变，命令字只有四个取值。看几个包就能认出来。
// 掩码这一层负责让包看起来不像任何东西。
package udpmask

import (
	"errors"
	"net"
	"strings"
)

// MaxPacketSize 是掩码层能处理的单包上限。
//
// 和上游的 finalmask.UDPSize 取一样的值。这个数不是 MTU——它是缓冲区
// 大小，要能装下明文加上所有掩码层的开销；真正决定发多大的是 mKCP 那边
// 的 MTU 设置。
const MaxPacketSize = 4096

var (
	ErrShortPacket  = errors.New("udpmask: 包长不足以容纳掩码头")
	ErrPacketTooBig = errors.New("udpmask: 包加上掩码开销后超过上限")
	ErrUnknownMask  = errors.New("udpmask: 未知的掩码类型")
)

// Mask 是一层掩码。
//
// 实现要保证 Wrap 之后的 PacketConn 在语义上仍是一个 PacketConn：
// 上层写进去多少明文，对端就读出多少明文。
type Mask interface {
	// Name 是配置里用的名字。
	Name() string
	// Overhead 是每个包会多出来的字节数。上层拿它来收缩有效载荷，
	// 免得加完掩码超过路径 MTU 被分片。
	Overhead() int
	// Wrap 把一个 PacketConn 包成带掩码的。
	Wrap(net.PacketConn) net.PacketConn
}

// New 按名字构造一层掩码。
//
// password 只有加密类的掩码会用到，伪装头类的忽略它。
func New(name, password string) (Mask, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "none", "mkcp-original":
		// mkcp-original 是上游给「不加任何掩码」起的名字。认它是为了让
		// 照着上游文档配的人不至于撞一个「未知掩码」的错。
		return nil, nil
	case "mkcp-aes128gcm", "aes128gcm", "aes-128-gcm":
		return NewAES128GCM(password)
	}
	return nil, ErrUnknownMask
}

// Wrap 按名字给 PacketConn 套一层掩码。名字为空时原样返回。
func Wrap(pc net.PacketConn, name, password string) (net.PacketConn, error) {
	mask, err := New(name, password)
	if err != nil {
		return nil, err
	}
	if mask == nil {
		return pc, nil
	}
	return mask.Wrap(pc), nil
}

// Overhead 返回某种掩码的每包开销，用来在算 MTU 时预留空间。
func Overhead(name, password string) (int, error) {
	mask, err := New(name, password)
	if err != nil {
		return 0, err
	}
	if mask == nil {
		return 0, nil
	}
	return mask.Overhead(), nil
}
