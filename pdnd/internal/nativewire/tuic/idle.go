package tuic

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errUDPIdleTimeout 是 UDP 会话空闲超时关闭的原因。
var errUDPIdleTimeout = errors.New("tuic UDP session idle timeout")

// idleTimeout 给 udpPacketConn 提供 sing canceler.PacketConn 所需的
// Timeout / SetTimeout（Pandora 改动）。
//
// 上游的 canceler.TimeoutPacketConn 每读一包都调一次 SetReadDeadline，
// 而 pipe.Deadline 每次都新建一个 time.AfterFunc 再停掉旧的：单连接几万包/秒
// 时这是可观的 CPU 与分配。这里逐包只记一个时间戳，单个定时器到期时再看是否
// 真的空闲，没空闲就按剩余时间重新上弦。收发任一方向有包都算活跃。
type idleTimeout struct {
	enabled  atomic.Bool
	last     atomic.Int64
	mu       sync.Mutex
	timeout  time.Duration
	timer    *time.Timer
	stopped  bool
	onExpire func()
}

func (t *idleTimeout) touch() {
	if t.enabled.Load() {
		t.last.Store(time.Now().UnixNano())
	}
}

func (t *idleTimeout) get() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timeout
}

func (t *idleTimeout) set(timeout time.Duration, onExpire func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.timeout, t.onExpire = timeout, onExpire
	t.last.Store(time.Now().UnixNano())
	t.enabled.Store(true)
	if t.timer == nil {
		t.timer = time.AfterFunc(timeout, t.fire)
	} else {
		t.timer.Reset(timeout)
	}
}

func (t *idleTimeout) fire() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	idle := time.Duration(time.Now().UnixNano() - t.last.Load())
	if idle < t.timeout {
		t.timer.Reset(t.timeout - idle)
		t.mu.Unlock()
		return
	}
	t.stopped = true
	onExpire := t.onExpire
	t.mu.Unlock()
	if onExpire != nil {
		onExpire()
	}
}

func (t *idleTimeout) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	t.enabled.Store(false)
	if t.timer != nil {
		t.timer.Stop()
	}
}

// Timeout 与 SetTimeout 让 canceler.NewPacketConn 直接用连接自带的空闲超时，
// 不再套一层逐包重设读截止时间的 TimeoutPacketConn。
func (c *udpPacketConn) Timeout() time.Duration { return c.idle.get() }

func (c *udpPacketConn) SetTimeout(timeout time.Duration) bool {
	if timeout <= 0 {
		// 非正值交还给 canceler 按它原来的语义处理。
		return false
	}
	c.idle.set(timeout, func() { c.closeWithError(errUDPIdleTimeout) })
	return true
}
