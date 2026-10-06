package mieru

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/enfein/mieru/v3/apis/client"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	M "github.com/sagernet/sing/common/metadata"
	"google.golang.org/protobuf/proto"
)

// ============================================================
//  夹具：会报 EMFILE 的监听器
// ============================================================

func emfileErr() error {
	return &net.OpError{Op: "accept", Net: "tcp", Err: os.NewSyscallError("accept", syscall.EMFILE)}
}

// emfileListener 前 failures 次 Accept 报 EMFILE，之后交给真实监听器。
type emfileListener struct {
	net.Listener
	failures atomic.Int32
	calls    atomic.Int32
}

func (l *emfileListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	if l.failures.Add(-1) >= 0 {
		return nil, emfileErr()
	}
	return l.Listener.Accept()
}

// emfileFactory 忽略 mux 要求的地址，改在 127.0.0.1 的随机端口上监听，
// 把监听器交回给测试，以便客户端去连。
type emfileFactory struct {
	failures int32
	ready    chan *emfileListener
}

func (f *emfileFactory) Listen(ctx context.Context, _, _ string) (net.Listener, error) {
	var lc net.ListenConfig
	inner, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &emfileListener{Listener: inner}
	l.failures.Store(f.failures)
	f.ready <- l
	return l, nil
}

// echoTransport 把所有 TCP 目标都拨到本地回显服务。
type echoTransport struct{ addr string }

func (t echoTransport) DialTCP(ctx context.Context, _ route.Meta, _ M.Socksaddr) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", t.addr)
}

func (echoTransport) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrUnexpectedEOF
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// ============================================================
//  端到端：EMFILE 之后入站仍能接客
// ============================================================

// TestInboundRecoversFromAcceptEMFILE 钉住：底层监听器连报几次 EMFILE 后，
// mieru 入站必须继续 Accept，并把下一条真实的 mieru 连接一路转发到目标。
// 修复前上游 mux 的 TCP Accept 循环遇错即 break，入站从此永久不再接客。
func TestInboundRecoversFromAcceptEMFILE(t *testing.T) {
	const uid = "00000000-0000-4000-8000-000000000001" // 虚构用户
	echoAddr := startEchoServer(t)

	factory := &emfileFactory{failures: 3, ready: make(chan *emfileListener, 1)}
	in := New("emfile-test", 1, "tcp", slog.New(slog.NewTextHandler(io.Discard, nil)))
	in.listenFactory = factory
	in.SetTransport(echoTransport{addr: echoAddr})
	if err := in.AddUsers([]core.User{{ID: 1, UUID: uid}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close() })

	var ln *emfileListener
	select {
	case ln = <-factory.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("mux 没有调用注入的监听工厂")
	}
	port := ln.Addr().(*net.TCPAddr).Port

	c := client.NewClient()
	if err := c.Store(&client.ClientConfig{
		Profile: &appctlpb.ClientProfile{
			ProfileName: proto.String("emfile-test"),
			User:        &appctlpb.User{Name: proto.String(uid), Password: proto.String(uid)},
			Servers: []*appctlpb.ServerEndpoint{{
				IpAddress: proto.String("127.0.0.1"),
				PortBindings: []*appctlpb.PortBinding{{
					Port:     proto.Int32(int32(port)),
					Protocol: appctlpb.TransportProtocol_TCP.Enum(),
				}},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var dst model.AddrSpec
	if err := dst.From("203.0.113.10:7"); err != nil { // TEST-NET-3，实际由 echoTransport 改拨回显服务
		t.Fatal(err)
	}
	conn, err := c.DialContext(ctx, model.NetAddrSpec{AddrSpec: dst, Net: "tcp"})
	if err != nil {
		t.Fatalf("经 mieru 建立代理连接失败（Accept 调用 %d 次）: %v", ln.calls.Load(), err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("写入失败（Accept 调用 %d 次）: %v", ln.calls.Load(), err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("EMFILE 之后入站没有恢复接客（Accept 调用 %d 次）: %v", ln.calls.Load(), err)
	}
	if string(buf) != "ping" {
		t.Fatalf("回显不符: %q", buf)
	}
	if got := ln.calls.Load(); got < 4 {
		t.Fatalf("Accept 只被调用 %d 次，三次 EMFILE 之后应当继续", got)
	}
}

// ============================================================
//  单元：retryListener 的退避与关闭
// ============================================================

// failingListener 的 Accept 在关闭前永远报 EMFILE，关闭后报 net.ErrClosed。
type failingListener struct {
	calls  atomic.Int32
	closes atomic.Int32
	done   chan struct{}
}

func newFailingListener() *failingListener { return &failingListener{done: make(chan struct{})} }

func (l *failingListener) Accept() (net.Conn, error) {
	l.calls.Add(1)
	select {
	case <-l.done:
		return nil, &net.OpError{Op: "accept", Net: "tcp", Err: net.ErrClosed}
	default:
		return nil, emfileErr()
	}
}

func (l *failingListener) Close() error {
	if l.closes.Add(1) == 1 {
		close(l.done)
	}
	return nil
}

func (l *failingListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestRetryListenerBacksOffAndClosesPromptly 钉住两件事：持续 EMFILE 时不空转
// （700ms 内按 5ms 起翻倍只该有 8 次左右 Accept），以及退避中的 Accept 在
// Close 后立刻返回 net.ErrClosed，而不是睡满当前这一段退避。
func TestRetryListenerBacksOffAndClosesPromptly(t *testing.T) {
	inner := newFailingListener()
	l := newRetryListener(inner, discardLogger())
	result := make(chan error, 1)
	go func() {
		_, err := l.Accept()
		result <- err
	}()

	// 700ms 时处在 635ms 起、长 640ms 的那一段退避里
	time.Sleep(700 * time.Millisecond)
	if got := inner.calls.Load(); got < 5 || got > 12 {
		t.Fatalf("700ms 内 Accept 调用 %d 次，退避不对（应约 8 次）", got)
	}
	select {
	case err := <-result:
		t.Fatalf("EMFILE 期间 Accept 不该返回: %v", err)
	default:
	}

	start := time.Now()
	_ = l.Close()
	_ = l.Close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("关闭后 Accept 应返回 net.ErrClosed，得到 %v", err)
		}
		if waited := time.Since(start); waited > 200*time.Millisecond {
			t.Fatalf("Close 后 Accept 过了 %v 才返回", waited)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 没有唤醒退避中的 Accept")
	}
	if got := inner.closes.Load(); got != 1 {
		t.Fatalf("内层监听器被关闭 %d 次，应为 1", got)
	}
}

// TestRetryListenerPassesClosedErrorThrough 钉住：内层已关时错误原样交回，
// 不退避、不重试，上游 mux 的循环得以照常收摊。
func TestRetryListenerPassesClosedErrorThrough(t *testing.T) {
	inner := newFailingListener()
	_ = inner.Close()
	l := newRetryListener(inner, discardLogger())
	start := time.Now()
	_, err := l.Accept()
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("应原样返回 net.ErrClosed，得到 %v", err)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("已关闭的监听器被 Accept %d 次，应为 1", got)
	}
	if waited := time.Since(start); waited > 50*time.Millisecond {
		t.Fatalf("关闭错误不该退避，实际等了 %v", waited)
	}
}

func TestNextAcceptBackoffSequence(t *testing.T) {
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 640, 1000, 1000}
	var d time.Duration
	for i, w := range want {
		d = nextAcceptBackoff(d)
		if d != w*time.Millisecond {
			t.Fatalf("第 %d 次退避 %v，应为 %v", i+1, d, w*time.Millisecond)
		}
	}
}
