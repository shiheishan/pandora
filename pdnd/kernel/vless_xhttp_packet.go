package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type xhttpPacketConn struct {
	ctx      context.Context
	uplink   *XHTTPPacketQueue
	downlink *XHTTPPacketQueue
	mu       sync.Mutex
	readBuf  []byte
	writeSeq uint64
	closed   bool
	// writeMu 让并发的 Write 串行：序号分配与入队必须在同一把锁里，否则高序号
	// 先占满下行队列、低序号进不去，读端等低序号——死锁（审查 X3）。它不保护
	// closed：Close 只拿 mu，能随时关队列唤醒等待中的写方。
	writeMu sync.Mutex
	// onAuth 在协议层读完请求头、清掉读截止时调用一次（见 SetReadDeadline）。
	onAuth   func()
	authOnce sync.Once
	// readDeadline（unix 纳秒，0 表示无）让协议层「10 秒内读不到请求头就断」在
	// 会话型 XHTTP 上也生效：只开了下行 GET、始终不送上行的会话不再永久挂着。
	readDeadline atomic.Int64
}

func newXHTTPPacketConn(ctx context.Context, duplex *XHTTPPacketDuplex) *xhttpPacketConn {
	return &xhttpPacketConn{ctx: ctx, uplink: duplex.Uplink, downlink: duplex.Downlink}
}

func (c *xhttpPacketConn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		ctx := c.ctx
		if deadline := c.readDeadline.Load(); deadline != 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, time.Unix(0, deadline))
			packet, err := c.uplink.Read(ctx)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) && c.ctx.Err() == nil {
					return 0, os.ErrDeadlineExceeded
				}
				return 0, err
			}
			c.readBuf = append(c.readBuf, packet.Payload...)
			continue
		}
		packet, err := c.uplink.Read(ctx)
		if err != nil {
			return 0, err
		}
		c.readBuf = append(c.readBuf, packet.Payload...)
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *xhttpPacketConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	seq := c.writeSeq
	c.writeSeq++
	c.mu.Unlock()
	// 下行满了就等客户端的 GET 取走，不报错：上游比客户端快是常态。
	// 锁不跨这次等待，Close 才能随时关队列把它唤醒。
	if err := c.downlink.PushWait(c.ctx, XHTTPPacket{Seq: seq, Payload: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *xhttpPacketConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	_ = c.uplink.Close(io.EOF)
	_ = c.downlink.Close(io.EOF)
	return nil
}

func (c *xhttpPacketConn) LocalAddr() net.Addr              { return xhttpAddr("pandora-xhttp-packet") }
func (c *xhttpPacketConn) RemoteAddr() net.Addr             { return xhttpAddr("xhttp-client") }
func (c *xhttpPacketConn) SetDeadline(t time.Time) error    { return c.SetReadDeadline(t) }
func (c *xhttpPacketConn) SetWriteDeadline(time.Time) error { return nil }

// SetReadDeadline 设读截止。协议层（vless / vmess）读请求头前设 10 秒截止、认证
// 通过后清零：清掉一个已设的截止即视为认证完成，会话从此不再计入未认证上限。
func (c *xhttpPacketConn) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		if c.readDeadline.Swap(0) != 0 && c.onAuth != nil {
			c.authOnce.Do(c.onAuth)
		}
	} else {
		c.readDeadline.Store(t.UnixNano())
	}
	return nil
}

func (a *vlessAdapter) startXHTTPPacketSession(id string, realitySession *RealitySession) (*xhttpSession, error) {
	if a.xhttpBroker == nil {
		return nil, fmt.Errorf("vless xhttp packet mode is not enabled")
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, fmt.Errorf("vless adapter closed")
	}
	if existing := a.xhttpSessions[id]; existing != nil {
		a.mu.Unlock()
		return existing, nil
	}
	duplex, err := a.xhttpBroker.OpenDuplex(id)
	if err != nil {
		a.mu.Unlock()
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	session := newXHTTPSession(duplex, ctx, cancel)
	a.xhttpSessions[id] = session
	// Register the worker while holding the adapter lock. Close() takes the
	// same lock before waiting, so a concurrent HTTP request cannot Add to the
	// WaitGroup after shutdown has begun.
	a.wg.Add(1)
	a.mu.Unlock()
	session.once.Do(func() {
		go func() {
			defer a.wg.Done()
			conn := newXHTTPPacketConn(ctx, duplex)
			conn.onAuth = func() { a.xhttpBroker.Authenticated(id) }
			err := a.handleConnSession(ctx, conn, realitySession)
			cancel()
			session.stopReaper()
			// 连同会话中转里的登记一起删：以前只关队列，每个会话在 broker 里
			// 留一对队列直到进程退出。
			_ = a.xhttpBroker.Close(id, err)
			_ = duplex.Uplink.Close(err)
			_ = duplex.Downlink.Close(err)
			a.mu.Lock()
			delete(a.xhttpSessions, id)
			a.mu.Unlock()
		}()
	})
	return session, nil
}

func (a *vlessAdapter) xhttpPacketHandler(ctx context.Context, session XHTTPSession) error {
	var realitySession *RealitySession
	if captured, ok := RealitySessionFromContext(ctx); ok {
		realitySession = &captured
	}
	return serveXHTTPSessionRequest(ctx, session, xhttpDownlinkGrace(a.xhttpConfig.Mode), func() (*xhttpSession, error) {
		return a.startXHTTPPacketSession(session.ID, realitySession)
	})
}
