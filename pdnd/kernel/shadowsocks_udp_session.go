package kernel

// Shadowsocks 经典 AEAD 的 UDP 按会话转发：同一 (客户端地址, 用户) 共用一个上游
// socket 与一个下行 goroutine。下行复用 Hysteria2 的 hy2DownlinkUDP（空闲不占收包
// 缓冲、借共享批量存货），写回经 ssUDPClientWriter 把包按 SS UDP 格式加密后写回
// 入站共享 UDP socket。空闲 ssUDPIdleTimeout（与 juicity、UoT 同值 5 分钟）无双向
// 流量则回收会话；每用户会话数受本入站的 udpSessionQuota 约束（每个入站一份，上限常量
// udpSessionsPerUser 与 hy2 / TUIC 相同）。建上游（ListenUDP，可能要做中转握手、解析域名）
// 在会话自己的 goroutine 里做，读循环只登记会话；建立期间到的包进会话的小队列。

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/time/rate"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ssUDPIdleTimeout 是一个 SS UDP 会话两个方向都没有数据多久就关（与 juicity、UoT 的 5 分钟同值）。测试缩短它。
var ssUDPIdleTimeout = 5 * time.Minute

// ssUDPPendingMax 是会话建立期间最多暂存的上行包数，多出的丢掉（计 udpDrops.pending）。
const ssUDPPendingMax = 8

type ssUDPPendingPacket struct {
	dest    vlessDestination
	payload []byte
}

// ssUDPSession 是一个 (客户端地址, 用户) 的 UDP 会话：一个上游 socket（经 DataPlane）、一个下行 goroutine。
type ssUDPSession struct {
	adapter   *shadowsocksAdapter
	client    netip.AddrPort // 已 Unmap 的客户端地址
	user      *ssUser        // 认证时的用户记录；retired 置位后快路径不再用它
	coreUser  core.User
	ip        string           // 在线设备的设备 IP（client.Addr().String()）
	firstDest vlessDestination // 首包目标：会话 goroutine 按它选出站、建上游
	// 建立期：upstream 为 nil 时到达的包进 pending（至多 ssUDPPendingMax 个，负载已复制）。
	// estMu 保护 upstream 的写入与 pending；ready 置位之后 upstream 不再变，读方不加锁直接用。
	estMu      sync.Mutex
	ready      atomic.Bool
	upstream   net.PacketConn
	pending    []ssUDPPendingPacket
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

// enqueuePending 在会话还没建好上游时把包放进小队列并返回 true（队列满就丢、计
// udpDrops.pending，同样返回 true）；上游已建好返回 false，由调用方直接写。
func (s *ssUDPSession) enqueuePending(dest vlessDestination, payload []byte) bool {
	s.estMu.Lock()
	defer s.estMu.Unlock()
	if s.upstream != nil {
		return false
	}
	if len(s.pending) >= ssUDPPendingMax {
		s.adapter.udpDrops.pending.Add(1)
		return true
	}
	s.pending = append(s.pending, ssUDPPendingPacket{
		dest:    dest,
		payload: append([]byte(nil), payload...),
	})
	return true
}

// lookupUDPSessions 把 client 名下仍活跃的会话追加到 dst 返回（已取消、还没收尾的
// 会话跳过；表里的切片会被收尾原地改写）。读循环传自己独占的 dst 复用，每包不分配。
func (a *shadowsocksAdapter) lookupUDPSessions(client netip.AddrPort, dst []*ssUDPSession) []*ssUDPSession {
	a.udpMu.Lock()
	for _, s := range a.udp[client] {
		if s.ctx.Err() == nil {
			dst = append(dst, s)
		}
	}
	a.udpMu.Unlock()
	return dst
}

// getOrOpenUDPSession 只登记会话（名额、在线设备、用户会话表、入表），不做任何 I/O；
// 整个过程持 udpMu，读循环与后续的慢路径 worker 并发处理同一 (地址, 用户) 的包时只会建出一个会话。
func (a *shadowsocksAdapter) getOrOpenUDPSession(client netip.AddrPort, user *ssUser, epoch uint64, firstDest vlessDestination) (*ssUDPSession, error) {
	a.udpMu.Lock()
	defer a.udpMu.Unlock()
	if a.udpClosed {
		return nil, net.ErrClosed
	}
	for _, s := range a.udp[client] {
		if s.user == user && s.ctx.Err() == nil {
			return s, nil
		}
	}
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
	s := &ssUDPSession{
		adapter: a, client: client, user: user, coreUser: coreUser, ip: ip,
		firstDest: firstDest, ctx: ctx, cancel: cancel,
	}
	s.lastActive.Store(time.Now().UnixNano())
	s.limiter = a.limiters.For(coreUser)
	s.sess = a.sessions.open(coreUser, epoch, s)
	if s.sess == nil {
		cancel()
		a.online.leave(coreUser, ip)
		a.udpQuota.release(user.ID)
		return nil, errSessionRevoked
	}
	for _, prev := range a.udp[client] {
		if prev.coreUser.ID == coreUser.ID {
			prev.cancel()
		}
	}
	a.udp[client] = append(a.udp[client], s)
	a.udpLive++
	a.wg.Add(1)
	go a.runUDPSession(s)
	return s, nil
}

// establishUDPSession 在会话 goroutine 里建上游。ListenUDP 可能要做中转握手、解析域名，
// 所以不能在读循环里调；ctx 取消（空闲、删人、关入站）会打断它。
func (a *shadowsocksAdapter) establishUDPSession(s *ssUDPSession) (net.PacketConn, error) {
	firstDest := s.firstDest
	meta := route.Meta{
		Domain: firstDest.Domain, IP: firstDest.IP, Port: firstDest.Port,
		Network: "udp", Protocol: "shadowsocks",
		SourceIP: s.client.Addr(), SourcePort: s.client.Port(),
	}
	upstream, err := a.plane.ListenUDP(s.ctx, meta, M.ParseSocksaddrHostPort(firstDest.Host, firstDest.Port))
	if err != nil {
		return nil, err
	}
	s.estMu.Lock()
	if s.ctx.Err() != nil {
		s.estMu.Unlock()
		_ = upstream.Close()
		return nil, s.ctx.Err()
	}
	s.upstream = upstream
	pending := s.pending
	s.pending = nil
	s.ready.Store(true)
	s.estMu.Unlock()
	for _, p := range pending {
		n, _ := s.writeUplink(p.dest, p.payload)
		if n > 0 {
			s.sess.up().Add(int64(n))
		}
	}
	return upstream, nil
}

// runUDPSession 在独立 goroutine 里先建上游，再跑下行：复用 hy2 的收包器（空闲时不占收包缓冲、借共享存货），
// 名额与份额与 hy2 / TUIC 全进程共用。
func (a *shadowsocksAdapter) runUDPSession(s *ssUDPSession) {
	defer a.wg.Done()
	defer a.finishUDPSession(s)
	upstream, err := a.establishUDPSession(s)
	if err != nil {
		a.connErr.addr(StageSession, net.UDPAddrFromAddrPort(s.client), err)
		return
	}
	u := newHy2UDPUpstream(upstream)
	share := hy2DownlinkBatchShares.join(s.coreUser.ID)
	defer hy2DownlinkBatchShares.leave(s.coreUser.ID)
	stop := context.AfterFunc(s.ctx, func() { _ = upstream.SetDeadline(time.Now()) })
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
	s.estMu.Lock()
	upstream := s.upstream
	s.pending = nil
	s.estMu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
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
