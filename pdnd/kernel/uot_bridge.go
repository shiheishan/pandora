package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// uotRoutedPacketConn is the UDP side of AnyTLS UoT. Each destination gets a
// routed DataPlane packet socket, while responses are fanned back into one
// packet stream for the UoT framing layer. This preserves per-datagram target
// addresses instead of binding the whole session to its first packet.
//
// 每个目标的上游 socket 占一个 fd、一个读 goroutine 和一块读缓冲，目标数由客户端
// 决定。所以一个目标就是一个 UDP 会话，与 hysteria2 / TUIC 同一口径：
//   - 占用户的 UDP 会话名额（udpSessionQuota，每用户 quicUDPSessionsPerUser 个，
//     同一入站上该用户的全部 UoT 流共用）；到上限时拒新目标、不踢旧，新目标的包
//     丢掉并经 onDrop 进观测链，整条 UoT 流照常；
//   - 发往某个目标失败（建不了上游、解析不了、被私网拦截等写错误）同样只丢这一个
//     包、经 onDrop 记一条，不关流：一个坏目标不连累同一条流上的其他目标；
//   - 空闲 uotUpstreamIdleTimeout（与 hy2 / TUIC 缺省 udpTimeout 同为 5 分钟）没有
//     收发就回收、归还名额，之后再发往同一目标会新建一个。
type uotRoutedPacketConn struct {
	ctx      context.Context
	cancel   context.CancelFunc
	plane    DataPlane
	baseMeta route.Meta
	limits   uotUDPLimits

	mu          sync.Mutex
	upstreams   map[string]*uotUpstream
	results     chan uotDatagram
	deadline    time.Time
	closed      bool
	janitorOnce sync.Once
	closeOnce   sync.Once
}

// uotUpstreamIdleTimeout 是 UoT 单个目标的空闲回收时长。
const uotUpstreamIdleTimeout = 5 * time.Minute

// errUOTTargetLimit：用户的 UDP 会话名额用完，这个新目标被拒。
var errUOTTargetLimit = errors.New("uot target limit")

// uotUDPLimits 是一条 UoT 流的资源约束。quota 为 nil 时不限目标数（只给测试）；
// idle 为 0 时用 uotUpstreamIdleTimeout。onDrop 在一个上行包被丢掉时调用，参数是
// 原因（名额用完时是 errUOTTargetLimit）。
type uotUDPLimits struct {
	quota  *udpSessionQuota
	userID int64
	onDrop func(error)
	idle   time.Duration
}

type uotUpstream struct {
	conn net.PacketConn
	// lastActive 是最近一次收或发的时间（UnixNano）。
	lastActive atomic.Int64
	// done 在目标被移出表时关闭：readUpstream 可能正阻塞在把回包交给 results 上
	// （客户端不读下行时），关 socket 叫不醒它，要靠 done 让它与名额归还同时退出，
	// 带走它的 goroutine 和读缓冲。
	done     chan struct{}
	doneOnce sync.Once
}

func newUOTUpstream(conn net.PacketConn) *uotUpstream {
	u := &uotUpstream{conn: conn, done: make(chan struct{})}
	u.touch()
	return u
}

// release 关 socket 并叫醒读 goroutine，可重复调用。
func (u *uotUpstream) release() {
	u.doneOnce.Do(func() { close(u.done) })
	_ = u.conn.Close()
}

func (u *uotUpstream) touch() { u.lastActive.Store(time.Now().UnixNano()) }

type uotDatagram struct {
	payload []byte
	addr    net.Addr
}

func newUOTRoutedPacketConn(parent context.Context, plane DataPlane, meta route.Meta, limits uotUDPLimits) *uotRoutedPacketConn {
	ctx, cancel := context.WithCancel(parent)
	if limits.idle <= 0 {
		limits.idle = uotUpstreamIdleTimeout
	}
	return &uotRoutedPacketConn{
		ctx: ctx, cancel: cancel, plane: plane, baseMeta: meta, limits: limits,
		upstreams: make(map[string]*uotUpstream), results: make(chan uotDatagram, 64),
	}
}

func (c *uotRoutedPacketConn) ensureUpstream(destination M.Socksaddr) (*uotUpstream, error) {
	key := destination.String()
	c.mu.Lock()
	if existing := c.upstreams[key]; existing != nil {
		// 在锁内 touch：回收方在锁内复查 lastActive，二者不会交错成「写方拿到目标、
		// 随即被回收、写到已关的 socket 上丢包」。
		existing.touch()
		c.mu.Unlock()
		return existing, nil
	}
	c.mu.Unlock()
	if q := c.limits.quota; q != nil && !q.acquire(c.limits.userID) {
		return nil, errUOTTargetLimit
	}
	meta := c.baseMeta
	meta.Domain, meta.IP, meta.Port = destination.Fqdn, destination.Addr, destination.Port
	conn, err := c.plane.ListenUDP(c.ctx, meta, destination)
	if err != nil {
		c.releaseQuota()
		return nil, err
	}
	upstream := newUOTUpstream(conn)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		c.releaseQuota()
		return nil, net.ErrClosed
	}
	if existing := c.upstreams[key]; existing != nil {
		existing.touch()
		c.mu.Unlock()
		_ = conn.Close()
		c.releaseQuota()
		return existing, nil
	}
	c.upstreams[key] = upstream
	c.mu.Unlock()
	c.janitorOnce.Do(func() { go c.reapIdle() })
	go c.readUpstream(key, upstream)
	return upstream, nil
}

func (c *uotRoutedPacketConn) releaseQuota() {
	if q := c.limits.quota; q != nil {
		q.release(c.limits.userID)
	}
}

// drop 把一个目标移出表、归还名额、关 socket 并叫醒它的读 goroutine。只有把它
// 移出表的那一方归还名额，回收、读出错、Close 三处并发时也只还一次。名额归还与
// 资源释放在同一时刻：还了名额，这个目标的 socket、读 goroutine 和读缓冲随即都走。
func (c *uotRoutedPacketConn) drop(key string, upstream *uotUpstream) {
	c.dropIdle(key, upstream, 0)
}

// dropIdle 只在目标到现在仍空闲（lastActive 早于 cutoff）时回收，复查在锁内做，
// 返回是否回收了；cutoff 为 0 时无条件回收（即 drop）。回收方在锁外列出的快照
// 可能已过时：快照之后刚有收发的目标留下。
func (c *uotRoutedPacketConn) dropIdle(key string, upstream *uotUpstream, cutoff int64) bool {
	c.mu.Lock()
	if cutoff != 0 && upstream.lastActive.Load() >= cutoff {
		c.mu.Unlock()
		return false
	}
	owned := c.upstreams[key] == upstream
	if owned {
		delete(c.upstreams, key)
	}
	c.mu.Unlock()
	if owned {
		c.releaseQuota()
	}
	upstream.release()
	return true
}

// reapIdle 定期回收空闲的目标，随整条 UoT 流的 ctx 结束。
func (c *uotRoutedPacketConn) reapIdle() {
	idle := c.limits.idle
	ticker := time.NewTicker(idle / 4)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			cutoff := now.Add(-idle).UnixNano()
			type entry struct {
				key string
				up  *uotUpstream
			}
			var stale []entry
			c.mu.Lock()
			for key, up := range c.upstreams {
				if up.lastActive.Load() < cutoff {
					stale = append(stale, entry{key, up})
				}
			}
			c.mu.Unlock()
			for _, e := range stale {
				c.dropIdle(e.key, e.up, cutoff)
			}
		}
	}
}

// readUpstream 把一个目标的回包汇进 results。读出错（被回收、Close、对端不可达）
// 只结束这一个目标，不连累同一 UoT 流上的其他目标。目标被移出表（done）时即使
// 正卡在交回包上也立刻退出，不等整条流关闭。
func (c *uotRoutedPacketConn) readUpstream(key string, upstream *uotUpstream) {
	defer c.drop(key, upstream)
	data := make([]byte, 64<<10)
	for {
		n, addr, err := upstream.conn.ReadFrom(data)
		if err != nil {
			return
		}
		upstream.touch()
		packet := uotDatagram{payload: append([]byte(nil), data[:n]...), addr: addr}
		select {
		case c.results <- packet:
		case <-upstream.done:
			return
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *uotRoutedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	c.mu.Lock()
	deadline := c.deadline
	c.mu.Unlock()
	var timer *time.Timer
	var timerC <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return 0, nil, netErrTimeout{}
		}
		timer = time.NewTimer(d)
		timerC = timer.C
		defer timer.Stop()
	}
	select {
	case <-c.ctx.Done():
		return 0, nil, net.ErrClosed
	case <-timerC:
		return 0, nil, netErrTimeout{}
	case result := <-c.results:
		if len(p) < len(result.payload) {
			return 0, nil, fmt.Errorf("uot packet exceeds read buffer")
		}
		return copy(p, result.payload), result.addr, nil
	}
}

// WriteTo 发往目标。发不出去（名额用完、目标无效、建不了上游、解析失败、写被拒）
// 时只丢这一个包、经 onDrop 记一条，返回 (0, nil)：UoT 的读循环遇到写错误会关
// 整条流，一个坏目标不该连累同一条流上的其他目标；返回 0 也让流量计数不把它记成
// 上行。
func (c *uotRoutedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	n, err := c.writeTo(p, addr)
	if err != nil {
		if c.limits.onDrop != nil {
			c.limits.onDrop(err)
		}
		return 0, nil
	}
	return n, nil
}

func (c *uotRoutedPacketConn) writeTo(p []byte, addr net.Addr) (int, error) {
	destination := M.SocksaddrFromNet(addr).Unwrap()
	if !destination.IsValid() || destination.Port == 0 {
		return 0, fmt.Errorf("uot destination is invalid")
	}
	upstream, err := c.ensureUpstream(destination)
	if err != nil {
		return 0, err
	}
	resolved, err := resolveUDPAddr(c.ctx, destination)
	if err != nil {
		return 0, err
	}
	upstream.touch()
	return upstream.conn.WriteTo(p, resolved)
}

func (c *uotRoutedPacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		upstreams := c.upstreams
		c.upstreams = make(map[string]*uotUpstream)
		c.mu.Unlock()
		c.cancel()
		for range upstreams {
			c.releaseQuota()
		}
		for _, upstream := range upstreams {
			upstream.release()
		}
	})
	return nil
}

func (c *uotRoutedPacketConn) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4zero} }

func (c *uotRoutedPacketConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	return nil
}
func (c *uotRoutedPacketConn) SetReadDeadline(deadline time.Time) error {
	return c.SetDeadline(deadline)
}
func (c *uotRoutedPacketConn) SetWriteDeadline(time.Time) error { return nil }

type netErrTimeout struct{}

func (netErrTimeout) Error() string   { return "i/o timeout" }
func (netErrTimeout) Timeout() bool   { return true }
func (netErrTimeout) Temporary() bool { return true }
