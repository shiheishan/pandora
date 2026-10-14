package kernel

import (
	"context"
	"errors"
	"net"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

// ssUDPResolve 与 ssUDPResolveTTL 是域名目标的解析函数与会话内缓存时长（与 hy2 同值）；做成变量只为测试替换。
var (
	ssUDPResolve    = resolveUDPAddr
	ssUDPResolveTTL = hy2UDPResolveTTL
)

// ssUDPUplinkMaxDebt 是 UDP 上行包最多能预支多长时间的令牌。同一用户的 TCP 与 UDP 下行在
// 同一个桶上 WaitN（先预支、桶变负数再睡），下行跑满时桶一直在 0 以下：上行若要求桶里先有
// 现成令牌就几乎全丢。上行在全入站共用的读循环里不能睡，所以预支不超过这个时长
// 就记债立即放行（等待摊给 WaitN 那一边，总速率仍受限），超过才丢。
const ssUDPUplinkMaxDebt = 50 * time.Millisecond

// allowUplink 按会话的限速器给 n 字节上行放行或丢弃，不阻塞。
func (s *ssUDPSession) allowUplink(n int) bool {
	if s.limiter == nil {
		return true
	}
	now := time.Now()
	r := s.limiter.ReserveN(now, n)
	if !r.OK() {
		return false
	}
	if r.DelayFrom(now) > ssUDPUplinkMaxDebt {
		r.CancelAt(now)
		return false
	}
	return true
}

// holdDevice 确认会话占着在线设备；之前因上行空闲让出过就重新 enter。返回 false 表示占不到
// （别的设备占满名额）或会话已收尾。
func (s *ssUDPSession) holdDevice() bool {
	if s.deviceHeld.Load() {
		return true
	}
	s.devMu.Lock()
	defer s.devMu.Unlock()
	if s.devDone {
		return false
	}
	if s.device {
		return true
	}
	if !s.adapter.online.enter(s.coreUser, s.ip) {
		return false
	}
	s.device = true
	s.deviceHeld.Store(true)
	return true
}

// releaseIdleDevice 在最近一次上行早于 cutoff（UnixNano）时让出在线设备，会话保留。
// 与 holdDevice 的无锁快查有一个很小的窗口：快查刚看到 deviceHeld 为真、这里随即让出，
// 那一个包不占设备放行，下一个包重新 enter。
func (s *ssUDPSession) releaseIdleDevice(cutoff int64) {
	s.devMu.Lock()
	defer s.devMu.Unlock()
	if s.device && s.lastUplink.Load() < cutoff {
		s.adapter.online.leave(s.coreUser, s.ip)
		s.device = false
		s.deviceHeld.Store(false)
	}
}

// releaseDevice 是收尾时的撤销：还占着就 leave，之后 holdDevice 一律返回 false。
func (s *ssUDPSession) releaseDevice() {
	s.devMu.Lock()
	defer s.devMu.Unlock()
	if s.device {
		s.adapter.online.leave(s.coreUser, s.ip)
	}
	s.device = false
	s.devDone = true
	s.deviceHeld.Store(false)
}

// forwardUDP 把明文 payload 发往 s 的上游。每个认证通过、归到本会话的上行包都算客户端
// 活跃（含被限速丢掉的）；设备名额让出过就在这里重新占，占不到就关会话。
func (a *shadowsocksAdapter) forwardUDP(s *ssUDPSession, dest vlessDestination, payload []byte) error {
	s.lastUplink.Store(time.Now().UnixNano())
	if !s.holdDevice() {
		s.cancel()
		return deviceLimitError("shadowsocks")
	}
	if !s.ready.Load() && s.enqueuePending(dest, payload) {
		return nil
	}
	n, err := s.writeUplink(dest, payload)
	if n > 0 {
		s.sess.up().Add(int64(n))
	}
	if err != nil && (errors.Is(err, net.ErrClosed) || s.ctx.Err() != nil) {
		return nil
	}
	return err
}

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

// writeUplink 把已解密的负载发往上游。上行在读循环、慢路径 worker 或会话 goroutine 里调用，
// 都不能阻塞：限速见 allowUplink。
func (s *ssUDPSession) writeUplink(dest vlessDestination, payload []byte) (int, error) {
	if !s.routeAllows(dest) {
		s.adapter.udpDrops.route.Add(1)
		return 0, nil
	}
	if !s.allowUplink(len(payload)) {
		s.adapter.udpDrops.rate.Add(1)
		return 0, nil
	}
	if dest.IP.IsValid() {
		ip := dest.IP.Unmap()
		addr := &net.UDPAddr{IP: ip.AsSlice(), Port: int(dest.Port)}
		n, err := s.upstream.WriteTo(payload, addr)
		return n, err
	}
	target := M.ParseSocksaddrHostPort(dest.Host, dest.Port)
	s.mu.Lock()
	if s.lastAddr != nil && s.lastDest == target && time.Since(s.resolved) < ssUDPResolveTTL {
		addr := s.lastAddr
		s.mu.Unlock()
		n, err := s.upstream.WriteTo(payload, addr)
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
		addr, err := ssUDPResolve(ctx, target)
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
		}
	}()
	return 0, nil
}
