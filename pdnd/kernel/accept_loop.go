package kernel

import (
	"errors"
	"net"
	"time"
)

// ============================================================
//  Accept 退避
// ============================================================

// Accept 出错后的等待区间，与 net/http.Server.Serve 同一套数：5ms 起翻倍，
// 封顶 1 秒，接到一条连接就归零。
//
// 为何需要：EMFILE / ENFILE（fd 用尽）时 Accept 会立刻再次失败。原来的
// 循环直接 continue，fd 用尽的那一刻节点反而把一个核跑满——正好是最需要
// CPU 去处理存量连接、等 fd 释放的时候。
//
// core/mieru/listener.go 有同值的一份（本包依赖 core/mieru，那边不能反向
// 引用这里），改这里的参数要一并改那边。
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// acceptBackoff 是退避状态机，零值即初始状态。
type acceptBackoff struct{ delay time.Duration }

// next 返回这一次该等多久，并把下一次翻倍。
func (b *acceptBackoff) next() time.Duration {
	if b.delay == 0 {
		b.delay = acceptBackoffMin
	} else {
		b.delay *= 2
	}
	if b.delay > acceptBackoffMax {
		b.delay = acceptBackoffMax
	}
	return b.delay
}

func (b *acceptBackoff) reset() { b.delay = 0 }

// ============================================================
//  Accept 循环
// ============================================================

// runAcceptLoop 反复 Accept，把每条连接交给 handle，直到 done 关闭或监听器
// 报 net.ErrClosed。
//
// 契约：
//   - handle 在循环自己的 goroutine 里同步调用，必须立刻返回——握手、读请求
//     这类会阻塞的事一律放进 handle 自己起的 goroutine。循环里阻塞一次，
//     一条只建 TCP 不说话的连接就能让整个入站停止接客。
//   - done 是关停信号（适配器 ctx.Done() 或监听器自己的 done）。适配器 Close
//     先 cancel 再关监听器，所以关停导致的 Accept 错误到这里时 done 已关。
//   - 其余错误一律退避重试，不区分 Temporary：既不像原来那样空转，也不因
//     一个没见过的错误让入站永久停止接客（监听器真坏了，代价也只是每秒一次
//     Accept）。
func runAcceptLoop(done <-chan struct{}, accept func() (net.Conn, error), handle func(net.Conn)) {
	runAcceptLoopWith(done, accept, handle, sleepUnlessDone)
}

// runAcceptLoopWith 是 runAcceptLoop 的可注入版本，sleep 返回 false 表示等待
// 期间 done 已关、循环应当退出。单测用它确定性地观察退避序列。
func runAcceptLoopWith(done <-chan struct{}, accept func() (net.Conn, error), handle func(net.Conn), sleep func(<-chan struct{}, time.Duration) bool) {
	var backoff acceptBackoff
	for {
		conn, err := accept()
		if err == nil {
			backoff.reset()
			handle(conn)
			continue
		}
		if isClosedChan(done) || errors.Is(err, net.ErrClosed) {
			return
		}
		if !sleep(done, backoff.next()) {
			return
		}
	}
}

// sleepUnlessDone 等 d，done 先关则提前返回 false。
func sleepUnlessDone(done <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return false
	case <-timer.C:
		return true
	}
}

func isClosedChan(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}
