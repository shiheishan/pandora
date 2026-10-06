// [INPUT]: 依赖 enfein/mieru 的 apicommon.StreamListenerFactory（protocol.Mux 的监听注入点），依赖 net 的 Listener 语义与 net.ErrClosed，依赖 log/slog
// [OUTPUT]: 包内提供 retryListenerFactory（把任一监听工厂产出的监听器包成 retryListener）、retryListener 与退避参数 acceptBackoffMin / acceptBackoffMax
// [POS]: core/mieru 的 Accept 韧性层：mieru.go 的 Start 经 Mux.SetStreamListenerFactory 把它塞进上游 mux，上游 TCP Accept 循环因此看不到 EMFILE 这类暂时错误；退避参数与 kernel/accept_loop.go 同一套

package mieru

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	apicommon "github.com/enfein/mieru/v3/apis/common"
)

// ============================================================
//  为何需要
// ============================================================
//
// 上游 mux（pkg/protocol/mux.go 的 acceptUnderlayLoop）的 TCP Accept 循环
// 遇到任何错误都 break，只记一条 Debug 日志。监听器不会被关、端口仍占着，
// 内核继续完成三次握手把连接塞进 backlog，却再没有人 Accept——fd 用尽
// （EMFILE / ENFILE）一次，这个 mieru 入站就永久静默失效，直到进程重启。
//
// 上游留了注入点 Mux.SetStreamListenerFactory，这里在它和真实监听器之间
// 夹一层：非关闭错误就地退避重试，只有监听器确实被关了才把错误交给上游，
// 让它的循环照常收摊。不改上游代码，也不用「失败就重建 mux」那种会踢掉
// 全部在线会话的办法。

// Accept 出错后的等待区间：5ms 起翻倍，封顶 1 秒，接到连接即归零。
// 与 kernel/accept_loop.go 的 acceptBackoffMin / acceptBackoffMax 同值；
// kernel 依赖本包（kernel/mieru.go），本包不能反向引用 kernel，只好各写一份。
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// ============================================================
//  监听工厂
// ============================================================

// retryListenerFactory 包住一个监听工厂，产出的监听器都带退避重试。
type retryListenerFactory struct {
	inner apicommon.StreamListenerFactory
	log   *slog.Logger
}

var _ apicommon.StreamListenerFactory = retryListenerFactory{}

func (f retryListenerFactory) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	l, err := f.inner.Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return newRetryListener(l, f.log), nil
}

// ============================================================
//  监听器
// ============================================================

// retryListener 的 Accept 把暂时错误吞在内部退避重试，只在关闭后返回错误。
//
// 退避状态放在每次 Accept 调用的栈上：一次调用要么拿到连接（等同归零），
// 要么因关闭返回，所以不需要跨调用的共享状态，并发 Accept 也无竞争。
type retryListener struct {
	net.Listener
	log       *slog.Logger
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newRetryListener(l net.Listener, log *slog.Logger) *retryListener {
	return &retryListener{Listener: l, log: log, done: make(chan struct{})}
}

func (l *retryListener) Accept() (net.Conn, error) {
	delay := time.Duration(0)
	retries := 0
	for {
		conn, err := l.Listener.Accept()
		if err == nil {
			if retries > 0 {
				l.log.Info("mieru Accept 已恢复", "重试次数", retries)
			}
			return conn, nil
		}
		if l.closed() || errors.Is(err, net.ErrClosed) {
			return nil, err
		}
		// 一段连续失败只在开头记一次 Warn：fd 用尽时每秒都在失败，逐次记会刷屏
		if retries == 0 {
			l.log.Warn("mieru Accept 失败，退避重试", "err", err)
		}
		retries++
		delay = nextAcceptBackoff(delay)
		if !l.sleep(delay) {
			return nil, &net.OpError{Op: "accept", Net: l.Addr().Network(), Addr: l.Addr(), Err: net.ErrClosed}
		}
	}
}

// Close 先发关停信号再关内层：正在退避的 Accept 立刻醒来返回，
// 正阻塞在内层 Accept 里的那一次则由内层的关闭唤醒。
func (l *retryListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

func (l *retryListener) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// sleep 等 d，期间被关闭则提前返回 false。
func (l *retryListener) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-l.done:
		return false
	case <-timer.C:
		return true
	}
}

// nextAcceptBackoff 由上一次的等待算出这一次：0 起步取下限，之后翻倍封顶。
func nextAcceptBackoff(prev time.Duration) time.Duration {
	if prev == 0 {
		return acceptBackoffMin
	}
	return min(prev*2, acceptBackoffMax)
}
