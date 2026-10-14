package kernel

import (
	"context"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// ============================================================
//  夹具：按脚本出结果的监听器
// ============================================================

// emfileErr 是 fd 用尽时 Accept 的真实错误形状。
var emfileErr = &net.OpError{Op: "accept", Net: "tcp", Err: syscall.EMFILE}

type acceptStep struct {
	conn net.Conn
	err  error
}

// scriptedListener 先按 steps 出结果；steps 空了以后，repeat 非空就一直返回
// repeat（模拟 fd 持续用尽），否则阻塞到 Close。Close 之后返回 net.ErrClosed。
type scriptedListener struct {
	steps  chan acceptStep
	repeat error
	calls  atomic.Int64
	closed chan struct{}
	once   sync.Once
}

func newScriptedListener(repeat error, steps ...acceptStep) *scriptedListener {
	l := &scriptedListener{steps: make(chan acceptStep, len(steps)+8), repeat: repeat, closed: make(chan struct{})}
	for _, s := range steps {
		l.steps <- s
	}
	return l
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
	}
	select {
	case s := <-l.steps:
		return s.conn, s.err
	default:
	}
	if l.repeat != nil {
		return nil, l.repeat
	}
	select {
	case s := <-l.steps:
		return s.conn, s.err
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *scriptedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// ============================================================
//  退避状态机与循环本身（确定性）
// ============================================================

func TestAcceptBackoffDoublesCapsAndResets(t *testing.T) {
	var b acceptBackoff
	var got []time.Duration
	for range 10 {
		got = append(got, b.next())
	}
	want := []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond,
		80 * time.Millisecond, 160 * time.Millisecond, 320 * time.Millisecond, 640 * time.Millisecond,
		time.Second, time.Second,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("退避序列 = %v，期望 %v", got, want)
	}
	b.reset()
	if d := b.next(); d != acceptBackoffMin {
		t.Fatalf("reset 后第一次退避 = %v，期望 %v", d, acceptBackoffMin)
	}
}

func TestRunAcceptLoopBacksOffResetsOnSuccessAndExitsOnClosed(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	ln := newScriptedListener(nil,
		acceptStep{err: emfileErr},
		acceptStep{err: emfileErr},
		acceptStep{err: emfileErr},
		acceptStep{conn: server},
		acceptStep{err: emfileErr},
		acceptStep{err: errors.New("没见过的错误也只退避，不退出")},
		acceptStep{err: net.ErrClosed},
	)
	var sleeps []time.Duration
	var handled []net.Conn
	done := make(chan struct{})
	runAcceptLoopWith(done, ln.Accept, func(c net.Conn) { handled = append(handled, c) }, func(_ <-chan struct{}, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return true
	})
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond}
	if !reflect.DeepEqual(sleeps, want) {
		t.Fatalf("退避 = %v，期望 %v（接到连接后归零）", sleeps, want)
	}
	if len(handled) != 1 || handled[0] != server {
		t.Fatalf("handle 收到 %d 条连接，期望恰好 1 条", len(handled))
	}
	if got := ln.calls.Load(); got != 7 {
		t.Fatalf("Accept 调用 %d 次，期望 7 次（遇到 net.ErrClosed 即停）", got)
	}
}

func TestRunAcceptLoopExitsWhenDone(t *testing.T) {
	done := make(chan struct{})
	close(done)
	ln := newScriptedListener(emfileErr)
	slept := false
	runAcceptLoopWith(done, ln.Accept, func(net.Conn) { t.Fatal("不应交出连接") }, func(<-chan struct{}, time.Duration) bool {
		slept = true
		return true
	})
	if slept {
		t.Fatal("done 已关时不应再退避")
	}
	if got := ln.calls.Load(); got != 1 {
		t.Fatalf("Accept 调用 %d 次，期望 1 次", got)
	}
}

func TestRunAcceptLoopExitsWhenSleepInterrupted(t *testing.T) {
	ln := newScriptedListener(emfileErr)
	runAcceptLoopWith(make(chan struct{}), ln.Accept, func(net.Conn) {}, func(<-chan struct{}, time.Duration) bool { return false })
	if got := ln.calls.Load(); got != 1 {
		t.Fatalf("Accept 调用 %d 次，期望退避被打断后不再 Accept", got)
	}
}

func TestSleepUnlessDoneWakesOnDone(t *testing.T) {
	done := make(chan struct{})
	go func() { time.Sleep(20 * time.Millisecond); close(done) }()
	start := time.Now()
	if sleepUnlessDone(done, time.Minute) {
		t.Fatal("done 关闭后应返回 false")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("done 关闭后 %v 才返回", elapsed)
	}
	if !sleepUnlessDone(make(chan struct{}), time.Millisecond) {
		t.Fatal("等满时长应返回 true")
	}
}

// ============================================================
//  适配器接线：fd 用尽时不空转，Close 能叫停
// ============================================================

// 每个用 runAcceptLoop 的 TCP 入站都在这里：Accept 持续 EMFILE 的 300ms 里，
// 退避下只该有个位数次调用（5+10+20+40+80+160ms），原来的空转是几十万次。
// 取消 ctx 后循环必须在退避等待中途醒来退出。
func TestAdapterAcceptLoopsBackOffOnEMFILE(t *testing.T) {
	cases := []struct {
		name  string
		start func(ctx context.Context, ln net.Listener) (wait func())
	}{
		{"vmess", func(ctx context.Context, ln net.Listener) func() {
			a := &vmessAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"vless", func(ctx context.Context, ln net.Listener) func() {
			a := &vlessAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"trojan", func(ctx context.Context, ln net.Listener) func() {
			a := &trojanAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"shadowsocks", func(ctx context.Context, ln net.Listener) func() {
			a := &shadowsocksAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"shadowsocks-2022", func(ctx context.Context, ln net.Listener) func() {
			a := &ss2022Adapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"shadowtls", func(ctx context.Context, ln net.Listener) func() {
			a := &shadowTLSAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"socks", func(ctx context.Context, ln net.Listener) func() {
			a := &proxyAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
		{"anytls", func(ctx context.Context, ln net.Listener) func() {
			a := &anyTLSAdapter{ctx: ctx, listener: ln, active: map[net.Conn]struct{}{}}
			a.wg.Add(1)
			go a.acceptLoop()
			return a.wg.Wait
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln := newScriptedListener(emfileErr)
			ctx, cancel := context.WithCancel(context.Background())
			wait := tc.start(ctx, ln)
			time.Sleep(300 * time.Millisecond)
			calls := ln.calls.Load()
			cancel()
			exited := make(chan struct{})
			go func() { wait(); close(exited) }()
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				t.Fatal("ctx 取消后 acceptLoop 没有退出")
			}
			if calls < 2 || calls > 20 {
				t.Fatalf("300ms 内 Accept 调用 %d 次，期望退避下为个位数", calls)
			}
		})
	}
}

// 退避之后接到的连接照常交给会话处理：VMess 收到非法请求头后读到超时再关
// （抗探测），net.Pipe 的写是同步的，两次写都被读走就说明会话在处理它。
func TestVMessAcceptLoopServesConnAfterEMFILE(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	ln := newScriptedListener(nil, acceptStep{err: emfileErr}, acceptStep{err: emfileErr}, acceptStep{conn: server})
	ctx, cancel := context.WithCancel(context.Background())
	a := &vmessAdapter{ctx: ctx, cancel: cancel, listener: ln, active: map[net.Conn]struct{}{}, users: map[string]vmessUser{}}
	a.wg.Add(1)
	go a.acceptLoop()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Write(make([]byte, 64)); err != nil {
		t.Fatalf("写请求头: %v", err)
	}
	if _, err := client.Write(make([]byte, 64)); err != nil {
		t.Fatalf("非法请求头之后会话应继续读空连接，实际 %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	if _, err := io.ReadAll(client); err != nil {
		t.Fatalf("Close 应关掉会话连接，实际 %v", err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 没有返回")
	}
	if got := ln.calls.Load(); got < 3 {
		t.Fatalf("Accept 调用 %d 次，期望至少 3 次", got)
	}
}

// REALITY 监听器自己的 acceptHandoff 以前遇到任何 Accept 错误都关掉 conns，
// 入站永久停止接客；现在 EMFILE 之后照常 Accept，只有 Close 让它停。
func TestRealityListenerSurvivesEMFILE(t *testing.T) {
	inner := newScriptedListener(nil, acceptStep{err: emfileErr}, acceptStep{err: emfileErr}, acceptStep{err: emfileErr})
	l := &RealityListener{inner: inner, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.acceptHandoff()
	accepted := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		accepted <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for inner.calls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := inner.calls.Load(); got < 4 {
		t.Fatalf("EMFILE 之后底层 Accept 只调用了 %d 次，监听器停止接客了", got)
	}
	select {
	case err := <-accepted:
		t.Fatalf("EMFILE 不该让 RealityListener.Accept 返回，实际返回 %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	_ = l.Close()
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("Close 之后 Accept 应返回错误")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 之后 Accept 没有返回")
	}
}

// VMess over mKCP 以前没把 mKCP 监听器记到 a.listener 上：acceptLoop 对着
// nil 监听器 Accept 直接 panic，整个进程退出。这里只要求能起、能停、端口能释放。
func TestVMessMKCPStartsAndCloses(t *testing.T) {
	port := reserveFreeUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"network": "mkcp"}}}
	adapter, err := NewDefaultAdapterRegistry().New(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), spec, AdapterHooks{DataPlane: &vlessTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- adapter.Close() }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("vmess mKCP 入站 Close 没有返回")
	}
	again, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("Close 之后 mKCP 端口没有释放：%v", err)
	}
	_ = again.Close()
}
