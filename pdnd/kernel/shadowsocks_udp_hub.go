package kernel

// 经典 SS 与 SS2022 共用的 UDP 会话表与会话生命周期，嵌入各自的适配器，
// 字段名沿用原来 shadowsocksAdapter 上的名字。

import (
	"context"
	"net"
	"net/netip"
	"sync"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

// ssUDPKey 是 UDP 会话表的键：经典 SS 按客户端地址（sid 为 0），SS2022 按客户端 session id（addr 为零值）。
type ssUDPKey struct {
	addr netip.AddrPort
	sid  uint64
}

// ssUDPHub 嵌入适配器（字段与方法被提升，a.udpMu 等写法照旧）。
type ssUDPHub struct {
	udpConn   *net.UDPConn                 // 入站 UDP socket，读循环与写回用
	udpMu     sync.Mutex                   // 保护 udp、udpLive、udpClosed
	udp       map[ssUDPKey][]*ssUDPSession // 会话表
	udpLive   int                          // 会话总数
	udpClosed bool                         // Close 之后不再建会话
	udpQuota  udpSessionQuota              // 每用户在途 UDP 会话上限
	udpDrops  ssUDPDropStats               // 各原因的丢包数
	udpJobs   chan ssUDPJob                // 慢路径有界队列

	// 以下由适配器在 Start 里经 bindUDP 设好，之后只读。
	udpProto    string // route.Meta.Protocol 与限额错误里的协议名
	udpCtx      context.Context
	udpWG       *sync.WaitGroup
	udpPlane    DataPlane
	udpErr      *connErrorReporter
	udpOnline   *onlineDevices
	udpLimiters *core.SpeedLimiters
	udpSessions *userSessions
	// udpEncode 把上游回包编成本方法的 SS UDP 包写进 dst（容量至少 ssUDPEncodeBufSize），返回 dst 的前缀。
	udpEncode func(s *ssUDPSession, dst []byte, source M.Socksaddr, payload []byte) ([]byte, error)
}

// ssUDPHubBinding 是 bindUDP 的参数。
type ssUDPHubBinding struct {
	proto    string
	ctx      context.Context
	wg       *sync.WaitGroup
	plane    DataPlane
	connErr  *connErrorReporter
	online   *onlineDevices
	limiters *core.SpeedLimiters
	sessions *userSessions
	encode   func(s *ssUDPSession, dst []byte, source M.Socksaddr, payload []byte) ([]byte, error)
}

// bindUDP 在 Start 里调用：记下入站 socket 与适配器的共用部件，建慢路径队列；udp 为 nil 时建表。
func (h *ssUDPHub) bindUDP(conn *net.UDPConn, b ssUDPHubBinding) {
	h.udpConn = conn
	h.udpProto = b.proto
	h.udpCtx = b.ctx
	h.udpWG = b.wg
	h.udpPlane = b.plane
	h.udpErr = b.connErr
	h.udpOnline = b.online
	h.udpLimiters = b.limiters
	h.udpSessions = b.sessions
	h.udpEncode = b.encode
	h.udpJobs = make(chan ssUDPJob, ssUDPSlowQueue)
	if h.udp == nil {
		h.udp = make(map[ssUDPKey][]*ssUDPSession)
	}
}

// findUDPSession 返回 key 下属于 user 且仍活跃的会话，没有返回 nil（SS2022 的快路径用，不分配）。
func (h *ssUDPHub) findUDPSession(key ssUDPKey, user *ssUser) *ssUDPSession {
	h.udpMu.Lock()
	defer h.udpMu.Unlock()
	for _, s := range h.udp[key] {
		if s.user == user && s.ctx.Err() == nil {
			return s
		}
	}
	return nil
}
