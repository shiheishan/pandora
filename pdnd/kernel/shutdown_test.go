package kernel

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 限时停机：大量活跃连接时关停在时限内完成（并行停 accept → 等排空 → 强制关），
// 交出的最后一轮流量包含这些在途连接已搬过的全部字节。
func TestCloseAndDrainWithManyActiveConnections(t *testing.T) {
	n := 10000
	switch {
	case raceEnabled || testing.Short():
		n = 1000
	case runtime.GOOS != "linux":
		// macOS 回环的临时端口只有 16K 个（含 TIME_WAIT），客户端与上游两段各占一份。
		n = 3000
	}
	// 每条连接在本进程里占 4 个 fd（客户端、入站、出站、回显）；fd 上限不够时缩量。
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil && int(lim.Cur) < n*4+1024 {
		n = (int(lim.Cur) - 1024) / 4
	}
	if n < 100 {
		t.Skip("fd 上限太低，跳过规模停机测试")
	}
	SetShutdownDrain(time.Second)
	defer SetShutdownDrain(DefaultShutdownDrain)

	p := lifecycleProtos()[0] // vless
	echo := startLifecycleEcho(t, false)
	users := make([]core.User, 50)
	for i := range users {
		users[i] = p.user(i)
	}
	c, port, tag := startLifecycleCore(t, p, users)
	conns := make([]net.Conn, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 128)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := p.dial(port, users[i%len(users)], echo.addr())
			if err == nil {
				err = echoOnce(conn, "0123456789")
			}
			if err != nil {
				errs <- fmt.Errorf("conn %d: %w", i, err)
				return
			}
			conns[i] = conn
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	defer func() {
		for _, conn := range conns {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	if live := c.liveSessionsForTest(tag); live != n {
		t.Fatalf("在途会话=%d，期望 %d", live, n)
	}

	start := time.Now()
	drained, err := c.CloseAndDrainTraffic()
	took := time.Since(start)
	t.Logf("%d 条活跃连接，关停耗时 %s", n, took)
	if err != nil {
		t.Fatal(err)
	}
	if took > 10*time.Second {
		t.Fatalf("关停耗时 %s，超过 10 秒", took)
	}
	var up, down int64
	for _, x := range drained[tag] {
		up += x.Upload
		down += x.Download
	}
	if want := int64(n * 10); up != want || down != want {
		t.Fatalf("最后一轮流量 上行 %d 下行 %d，期望各 %d", up, down, want)
	}
	// 客户端随后都能看到连接被关（不是一直挂着）。
	_ = conns[0].SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conns[0].Read(make([]byte, 1)); err == nil {
		t.Fatal("关停后客户端连接应已断开")
	}
}

// 停机先并行停 accept：排空期间新连接进不来，已有连接照常可用。
func TestShutdownStopsAcceptingBeforeDrain(t *testing.T) {
	SetShutdownDrain(800 * time.Millisecond)
	defer SetShutdownDrain(DefaultShutdownDrain)
	p := lifecycleProtos()[0]
	echo := startLifecycleEcho(t, false)
	user := p.user(0)
	c, port, _ := startLifecycleCore(t, p, []core.User{user})
	conn, err := p.dial(port, user, echo.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := echoOnce(conn, "before"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = c.CloseAndDrainTraffic()
		close(done)
	}()
	ok, why := waitFor(500*time.Millisecond, func() (bool, string) {
		probe, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return true, ""
		}
		_ = probe.Close()
		return false, "监听仍在接客"
	})
	if !ok {
		t.Fatal(why)
	}
	if err := echoOnce(conn, "draining"); err != nil {
		t.Fatalf("排空期间已有连接应照常可用：%v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("关停没有在排空时限后完成")
	}
}

// stuckAdapter 的 Close 先关监听，然后永远卡住（模拟旧连接排不空）。
type stuckAdapter struct {
	ln      net.Listener
	release chan struct{}
}

func (a *stuckAdapter) Protocol() string           { return "stuck" }
func (a *stuckAdapter) Validate(InboundSpec) error { return nil }
func (a *stuckAdapter) Start(_ context.Context, spec InboundSpec, _ AdapterHooks) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", spec.Config.Port))
	a.ln = ln
	return err
}
func (a *stuckAdapter) Close() error {
	if a.ln != nil {
		_ = a.ln.Close()
	}
	<-a.release
	return nil
}
func (a *stuckAdapter) AddUsers([]core.User) error                   { return nil }
func (a *stuckAdapter) UpsertUsers([]core.User) error                { return nil }
func (a *stuckAdapter) DelUsers([]string) error                      { return nil }
func (a *stuckAdapter) SnapshotTraffic() ([]core.UserTraffic, error) { return nil, nil }
func (a *stuckAdapter) OnlineIPs() map[int64][]string                { return nil }

// 改配置卡死（1c1g 实测，致命）：旧入站的 Close 排不空时，入站重建不能跟着
// 永久卡住——限时后放手，新入站照常在同一端口起来。
func TestApplyInboundDoesNotHangOnStuckPreviousAdapter(t *testing.T) {
	adapterCloseTimeout.Store(int64(200 * time.Millisecond))
	defer adapterCloseTimeout.Store(int64(defaultAdapterClose))
	release := make(chan struct{})
	defer close(release)
	registry := NewAdapterRegistry()
	_ = registry.Register("stuck", func(InboundSpec) (Adapter, error) { return &stuckAdapter{release: release}, nil })
	c := NewNativeCore(registry)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	reserved, _ := net.Listen("tcp", "127.0.0.1:0")
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	cfg := &core.InboundConfig{Tag: "stuck-1", Protocol: "stuck", Port: port}
	if err := c.ApplyInbound(cfg, nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- c.ApplyInbound(&core.InboundConfig{Tag: "stuck-1", Protocol: "stuck", Port: port, Raw: map[string]any{"v": 2.0}}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("重建入站失败：%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("旧入站排不空时，入站重建卡住了")
	}
	if err := c.InboundReady("stuck-1"); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { _ = c.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("内核关停被卡住的入站拖住了")
	}
}
