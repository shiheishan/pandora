package kernel

// Shadowsocks 经典 AEAD 的 UDP 按会话转发：同一 (客户端地址, 用户) 共用一个上游
// socket 与一个下行 goroutine。下行复用 Hysteria2 的 hy2DownlinkUDP（空闲不占收包
// 缓冲、借共享批量存货），写回经 ssUDPClientWriter 把包按 SS UDP 格式加密后写回
// 入站共享 UDP socket。空闲 ssUDPIdleTimeout（与 juicity、UoT 同值 5 分钟）无双向
// 流量则回收会话；每用户会话数受 udpSessionQuota 约束，与 hy2 / TUIC 全进程共用上限。

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/time/rate"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ssUDPIdleTimeout 是一个 SS UDP 会话两个方向都没有数据多久就关（与 juicity、UoT 的 5 分钟同值）。测试缩短它。
var ssUDPIdleTimeout = 5 * time.Minute

// ssUDPSession 是一个 (客户端地址, 用户) 的 UDP 会话：一个上游 socket（经 DataPlane）、一个下行 goroutine。
type ssUDPSession struct {
	adapter    *shadowsocksAdapter
	client     netip.AddrPort // 已 Unmap 的客户端地址
	user       *ssUser        // 认证时的用户记录；retired 置位后快路径不再用它
	coreUser   core.User
	ip         string // 在线设备的设备 IP（client.Addr().String()）
	upstream   net.PacketConn
	sess       *userSession
	limiter    *rate.Limiter // 认证时取一次 a.limiters.For(coreUser)，nil 表示不限速
	ctx        context.Context
	cancel     context.CancelFunc
	lastActive atomic.Int64 // UnixNano，上行与下行都刷新
	// 域名目标的解析缓存（只缓存最近一个目标），mu 保护；resolving 为真时有一次解析在途。
	mu        sync.Mutex
	lastDest  M.Socksaddr
	lastAddr  *net.UDPAddr
	resolved  time.Time
	resolving bool
}

func (s *ssUDPSession) Close() error { s.cancel(); return nil } // 给 userSessions 踢人用

// lookupUDPSessions 把 client 名下的会话追加到 dst 返回（拷贝：表里的切片会被收尾原地
// 改写）。读循环传自己独占的 dst 复用，每包不分配。
func (a *shadowsocksAdapter) lookupUDPSessions(client netip.AddrPort, dst []*ssUDPSession) []*ssUDPSession {
	a.udpMu.Lock()
	dst = append(dst, a.udp[client]...)
	a.udpMu.Unlock()
	return dst
}

// openUDPSession 在读循环里同步建会话：名额 → 在线设备 → 上游 socket → 用户会话表，
// 任一步失败撤销前面已做的。会话的出站按首包目标选（与 hy2 / TUIC 同），之后发往
// 别的目标的包走同一个上游 socket（私网拦截仍逐包判定）。
func (a *shadowsocksAdapter) openUDPSession(client netip.AddrPort, user *ssUser, epoch uint64, firstDest vlessDestination) (*ssUDPSession, error) {
	coreUser := core.User{ID: user.ID, DeviceLimit: user.DeviceLimit, SpeedLimit: user.SpeedLimit}
	if !a.udpQuota.acquire(user.ID) {
		return nil, udpSessionLimitError("shadowsocks")
	}
	ip := client.Addr().String()
	if !a.online.enter(coreUser, ip) {
		a.udpQuota.release(user.ID)
		return nil, deviceLimitError("shadowsocks")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	meta := route.Meta{
		Domain: firstDest.Domain, IP: firstDest.IP, Port: firstDest.Port,
		Network: "udp", Protocol: "shadowsocks",
		SourceIP: client.Addr(), SourcePort: client.Port(),
	}
	upstream, err := a.plane.ListenUDP(ctx, meta, M.ParseSocksaddrHostPort(firstDest.Host, firstDest.Port))
	if err != nil {
		cancel()
		a.online.leave(coreUser, ip)
		a.udpQuota.release(user.ID)
		return nil, err
	}
	s := &ssUDPSession{
		adapter: a, client: client, user: user, coreUser: coreUser, ip: ip,
		upstream: upstream, ctx: ctx, cancel: cancel,
	}
	s.lastActive.Store(time.Now().UnixNano())
	s.limiter = a.limiters.For(coreUser)
	s.sess = a.sessions.open(coreUser, epoch, s)
	if s.sess == nil {
		_ = upstream.Close()
		cancel()
		a.online.leave(coreUser, ip)
		a.udpQuota.release(user.ID)
		return nil, errSessionRevoked
	}
	var old []*ssUDPSession
	a.udpMu.Lock()
	if a.udpClosed {
		a.udpMu.Unlock()
		s.sess.close()
		_ = upstream.Close()
		cancel()
		a.online.leave(coreUser, ip)
		a.udpQuota.release(user.ID)
		return nil, net.ErrClosed
	}
	// 同一地址、同一用户 ID 的旧会话（用户记录被替换后的残留）只取消：它的下行
	// goroutine 退出时由 finishUDPSession 摘表、计数减一；在那之前快路径见它已
	// retired 就跳过。
	for _, prev := range a.udp[client] {
		if prev.coreUser.ID == coreUser.ID {
			old = append(old, prev)
		}
	}
	a.udp[client] = append(a.udp[client], s)
	a.udpLive++
	a.udpMu.Unlock()
	for _, o := range old {
		o.cancel()
	}
	a.wg.Add(1)
	go a.runUDPSession(s)
	return s, nil
}

// runUDPSession 在独立 goroutine 里跑下行：复用 hy2 的收包器（空闲时不占收包缓冲、借共享存货），
// 名额与份额与 hy2 / TUIC 全进程共用。
func (a *shadowsocksAdapter) runUDPSession(s *ssUDPSession) {
	defer a.wg.Done()
	defer a.finishUDPSession(s)
	u := newHy2UDPUpstream(s.upstream)
	share := hy2DownlinkBatchShares.join(s.coreUser.ID)
	defer hy2DownlinkBatchShares.leave(s.coreUser.ID)
	stop := context.AfterFunc(s.ctx, func() { _ = s.upstream.SetDeadline(time.Now()) })
	defer stop()
	hy2DownlinkUDP(s.ctx, ssUDPClientWriter{s: s}, u, share, s.sess.down())
}

// finishUDPSession 是会话唯一的收尾处（只由 runUDPSession 的 defer 调用）：摘表、关
// 上游、还名额、撤在线设备、注销用户会话。
func (a *shadowsocksAdapter) finishUDPSession(s *ssUDPSession) {
	s.cancel()
	a.udpMu.Lock()
	list := a.udp[s.client]
	for i, x := range list {
		if x == s {
			list = append(list[:i], list[i+1:]...)
			a.udpLive--
			break
		}
	}
	if len(list) == 0 {
		delete(a.udp, s.client)
	} else {
		a.udp[s.client] = list
	}
	a.udpMu.Unlock()
	_ = s.upstream.Close()
	a.udpQuota.release(s.coreUser.ID)
	a.online.leave(s.coreUser, s.ip)
	s.sess.close()
}

// closeUDPSessions 在入站 Close 时取消全部会话，之后不再建新会话；各会话的
// goroutine 自己收尾，Close 随后的 wg.Wait 等它们。
func (a *shadowsocksAdapter) closeUDPSessions() {
	var all []*ssUDPSession
	a.udpMu.Lock()
	a.udpClosed = true
	for _, list := range a.udp {
		all = append(all, list...)
	}
	a.udpMu.Unlock()
	for _, s := range all {
		s.cancel()
	}
}

// udpJanitor 每 ssUDPIdleTimeout/4 扫一遍，取消两个方向都空闲满 ssUDPIdleTimeout 的会话。
func (a *shadowsocksAdapter) udpJanitor() {
	defer a.wg.Done()
	interval := ssUDPIdleTimeout / 4
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	idle := int64(ssUDPIdleTimeout)
	for {
		select {
		case <-a.ctx.Done():
			return
		case now := <-ticker.C:
			nowNano := now.UnixNano()
			var stale []*ssUDPSession
			a.udpMu.Lock()
			for _, list := range a.udp {
				for _, s := range list {
					if nowNano-s.lastActive.Load() >= idle {
						stale = append(stale, s)
					}
				}
			}
			a.udpMu.Unlock()
			for _, s := range stale {
				s.cancel()
			}
		}
	}
}

// writeUplink 把已解密的负载发往上游。读循环所有用户共用，限速超速时丢包，不能 WaitN 阻塞。
func (s *ssUDPSession) writeUplink(dest vlessDestination, payload []byte) (int, error) {
	if s.limiter != nil && !s.limiter.AllowN(time.Now(), len(payload)) {
		return 0, nil
	}
	if dest.IP.IsValid() {
		ip := dest.IP.Unmap()
		addr := &net.UDPAddr{IP: ip.AsSlice(), Port: int(dest.Port)}
		n, err := s.upstream.WriteTo(payload, addr)
		if n > 0 {
			s.lastActive.Store(time.Now().UnixNano())
		}
		return n, err
	}
	target := M.ParseSocksaddrHostPort(dest.Host, dest.Port)
	s.mu.Lock()
	if s.lastAddr != nil && s.lastDest == target && time.Since(s.resolved) < hy2UDPResolveTTL {
		addr := s.lastAddr
		s.mu.Unlock()
		n, err := s.upstream.WriteTo(payload, addr)
		if n > 0 {
			s.lastActive.Store(time.Now().UnixNano())
		}
		return n, err
	}
	// 域名没命中缓存：解析另起 goroutine 做，不能在所有用户共用的读循环里阻塞；同一
	// 会话同时只解一个，解析期间到的包丢掉（UDP 允许丢包，客户端会重发）。
	if s.resolving {
		s.mu.Unlock()
		return 0, nil
	}
	s.resolving = true
	copyPayload := append([]byte(nil), payload...)
	s.mu.Unlock()
	s.adapter.wg.Add(1)
	go func() {
		defer s.adapter.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		addr, err := resolveUDPAddr(ctx, target)
		s.mu.Lock()
		s.resolving = false
		if err == nil {
			s.lastDest, s.lastAddr, s.resolved = target, addr, time.Now()
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
		if n, _ := s.upstream.WriteTo(copyPayload, addr); n > 0 {
			s.sess.up().Add(int64(n))
			s.lastActive.Store(time.Now().UnixNano())
		}
	}()
	return 0, nil
}

// ssUDPClientWriter 是交给 hy2DownlinkUDP 的「客户端连接」：WritePacket 把上游回包按 SS UDP 格式
// 加密（salt + AEAD(地址 + 负载)），写回入站 UDP socket 上的客户端地址。只用到 WritePacket。
type ssUDPClientWriter struct{ s *ssUDPSession }

var _ N.PacketConn = ssUDPClientWriter{}

func (w ssUDPClientWriter) WritePacket(buffer *buf.Buffer, source M.Socksaddr) error {
	defer buffer.Release()
	payload := buffer.Bytes()
	s := w.s
	if s.limiter != nil {
		if err := s.limiter.WaitN(s.ctx, len(payload)); err != nil {
			return err
		}
	}
	bp := ssUDPEncodePool.Get().(*[]byte)
	out, err := s.adapter.encodeUDPPacket(*bp, s.user.MasterKey, source, payload)
	if err == nil {
		_, err = s.adapter.udpConn.WriteToUDPAddrPort(out, s.client)
	}
	ssUDPEncodePool.Put(bp)
	if err == nil {
		s.lastActive.Store(time.Now().UnixNano())
	}
	return err
}

func (w ssUDPClientWriter) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, os.ErrInvalid
}

func (w ssUDPClientWriter) Close() error {
	w.s.cancel()
	return nil
}

func (w ssUDPClientWriter) LocalAddr() net.Addr {
	return w.s.adapter.udpConn.LocalAddr()
}

func (w ssUDPClientWriter) SetDeadline(t time.Time) error      { return nil }
func (w ssUDPClientWriter) SetReadDeadline(t time.Time) error  { return nil }
func (w ssUDPClientWriter) SetWriteDeadline(t time.Time) error { return nil }
