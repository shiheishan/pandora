package kernel

import (
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 限时停机与入站退场（10 万连接实测：SIGTERM 4 分钟没退出；1c1g 实测：改端口
// 触发入站重建时在旧适配器的 WaitGroup 上永久卡住，新监听起不来、用户同步与
// 上报全部停摆）。
//
// 原先 CloseAndDrainTraffic 按顺序逐个 Close 入站，Close 先关在途连接再
// wg.Wait——只要有一条连接的转发挂住，后面的入站根本轮不到关、继续接客，
// 进程也就一直不退。现在：
//
//  1. 并行停止所有入站的 accept（只关监听，在途连接照常）；
//  2. 等在途会话自然结束，最多 shutdownDrain（默认 5 秒，可配）；
//  3. 并行强制关闭（Close 关掉全部连接）；每个 Close 最多等 adapterCloseTimeout，
//     到点不再等，记一条告警继续往下走。
//
// 流量随搬随计（core.Relay 的原子计数），所以无论第 3 步有没有等完，取到的
// 都是已经搬过的全部字节，最后一次上报不丢在途连接的流量。

var (
	shutdownDrain       atomic.Int64
	adapterCloseTimeout atomic.Int64
)

const (
	// DefaultShutdownDrain 是停机时等在途连接自然结束的上限。
	DefaultShutdownDrain = 5 * time.Second
	defaultAdapterClose  = 3 * time.Second
)

func init() {
	shutdownDrain.Store(int64(DefaultShutdownDrain))
	adapterCloseTimeout.Store(int64(defaultAdapterClose))
}

// SetShutdownDrain 设置停机时等在途连接自然结束的上限（pdnd 配置
// shutdown_drain_seconds）。0 表示不等、直接强制关闭。
func SetShutdownDrain(d time.Duration) {
	if d < 0 {
		d = 0
	}
	shutdownDrain.Store(int64(d))
}

// acceptStopper 由 TCP 类适配器实现：只关监听、不碰在途连接。QUIC 类入站关监听
// 就等于关掉全部连接，不实现它，留到强制关闭那一步。
type acceptStopper interface {
	stopAccepting()
}

var errAdapterCloseTimeout = errors.New("适配器关闭超时，已不再等待")

// closeAdapterBounded 关一个适配器，最多等 adapterCloseTimeout。超时后 Close 在
// 后台继续（它的监听已先关掉，不会挡住同端口的新入站）。
func closeAdapterBounded(a Adapter) error {
	done := make(chan error, 1)
	go func() { done <- a.Close() }()
	timeout := time.Duration(adapterCloseTimeout.Load())
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		slog.Warn("入站关闭超时，不再等待其在途连接收尾", "protocol", a.Protocol(), "等待", timeout)
		return errAdapterCloseTimeout
	}
}

// stopAcceptingAll 并行让所有入站停止接客。
func stopAcceptingAll(inbounds map[string]*nativeInbound) {
	var wg sync.WaitGroup
	for _, in := range inbounds {
		in.mu.RLock()
		adapter := in.adapter
		in.mu.RUnlock()
		stopper, ok := adapter.(acceptStopper)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopper.stopAccepting()
		}()
	}
	wg.Wait()
}

// waitSessionsDrained 等所有入站的在途会话归零，最多 limit。返回剩余会话数。
func waitSessionsDrained(inbounds map[string]*nativeInbound, limit time.Duration) int {
	deadline := time.Now().Add(limit)
	for {
		live := 0
		for _, in := range inbounds {
			in.mu.RLock()
			live += liveSessionsOf(in.adapter)
			in.mu.RUnlock()
		}
		if live == 0 || !time.Now().Before(deadline) {
			return live
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// CloseAndDrainTraffic 关停内核并交出每个入站最后一轮流量。重复调用返回空表。
func (c *NativeCore) CloseAndDrainTraffic() (map[string][]core.UserTraffic, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return map[string][]core.UserTraffic{}, nil
	}
	c.closed = true
	inbounds := make(map[string]*nativeInbound, len(c.inbounds))
	for tag, in := range c.inbounds {
		inbounds[tag] = in
		delete(c.inbounds, tag)
	}
	c.ports = make(map[portKey]string)
	c.mu.Unlock()

	start := time.Now()
	stopAcceptingAll(inbounds)
	left := waitSessionsDrained(inbounds, time.Duration(shutdownDrain.Load()))

	var (
		wg      sync.WaitGroup
		errMu   sync.Mutex
		first   error
		timeout int
	)
	for tag, in := range inbounds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := closeNativeInbound(in)
			c.stashRetiredTraffic(tag, in)
			if err != nil {
				errMu.Lock()
				if errors.Is(err, errAdapterCloseTimeout) {
					timeout++
				}
				if first == nil {
					first = err
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	c.connErrors.Close()
	if left > 0 || timeout > 0 {
		slog.Info("内核已关停", "强制断开的在途会话", left, "关闭超时的入站", timeout, "耗时", time.Since(start).Round(time.Millisecond))
	}

	c.mu.Lock()
	out := c.retiredTraffic
	c.retiredTraffic = make(map[string][]core.UserTraffic)
	c.mu.Unlock()
	return out, first
}

// ============================================================
//  各 TCP 类适配器的「只关监听」
// ============================================================

func closeListener(mu *sync.RWMutex, l *net.Listener) {
	mu.RLock()
	ln := *l
	mu.RUnlock()
	if ln != nil {
		_ = ln.Close()
	}
}

func (a *vlessAdapter) stopAccepting()       { closeListener(&a.mu, &a.listener) }
func (a *vmessAdapter) stopAccepting()       { closeListener(&a.mu, &a.listener) }
func (a *trojanAdapter) stopAccepting()      { closeListener(&a.mu, &a.listener) }
func (a *shadowsocksAdapter) stopAccepting() { closeListener(&a.mu, &a.listener) }
func (a *ss2022Adapter) stopAccepting()      { closeListener(&a.mu, &a.listener) }
func (a *proxyAdapter) stopAccepting()       { closeListener(&a.mu, &a.listener) }
func (a *naiveAdapter) stopAccepting()       { closeListener(&a.mu, &a.listener) }
func (a *anyTLSAdapter) stopAccepting()      { closeListener(&a.mu, &a.listener) }
func (a *shadowTLSAdapter) stopAccepting()   { closeListener(&a.mu, &a.listener) }
