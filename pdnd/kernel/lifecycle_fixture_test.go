package kernel

// 连接生命周期测试共用的夹具：经真实 NativeCore（含出站租约连接）把各协议的
// 客户端连到本机回显上游，供「客户端断开即释放」「删用户即断线」「流量按周期
// 计入」「限时停机」几组测试共用。用户与口令全是虚构的。

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	SS "github.com/sagernet/sing-shadowsocks2"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
)

// lifecycleProto 描述一个协议怎么配、怎么当客户端连上来。
type lifecycleProto struct {
	name string
	raw  map[string]any
	// user 生成第 i 个虚构用户。
	user func(i int) core.User
	// dial 建一条到 target 的代理连接，返回可读写的应用层流。
	dial func(port int, user core.User, target *net.TCPAddr) (net.Conn, error)
}

func lifecycleProtos() []lifecycleProto {
	return []lifecycleProto{
		{
			name: "vless",
			raw:  map[string]any{"network": "tcp"},
			user: func(i int) core.User { return core.User{ID: int64(5000 + i), UUID: uuid.NewString()} },
			dial: dialLifecycleVLESS,
		},
		{
			name: "vmess",
			raw:  map[string]any{"security": "aes-128-gcm"},
			user: func(i int) core.User { return core.User{ID: int64(5000 + i), UUID: uuid.NewString()} },
			dial: dialLifecycleVMess,
		},
		{
			name: "trojan",
			raw:  map[string]any{},
			user: func(i int) core.User {
				return core.User{ID: int64(5000 + i), UUID: fmt.Sprintf("lifecycle-trojan-%d", i)}
			},
			dial: dialLifecycleTrojan,
		},
		{
			name: "shadowsocks",
			raw:  map[string]any{"method": "aes-128-gcm"},
			user: func(i int) core.User { return core.User{ID: int64(5000 + i), UUID: fmt.Sprintf("lifecycle-ss-%d", i)} },
			dial: dialLifecycleSS,
		},
	}
}

func dialLifecycleVLESS(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	id := uuid.MustParse(user.UUID)
	header := []byte{vlessVersion}
	header = append(header, id[:]...)
	header = append(header, 0, vlessTCP, byte(target.Port>>8), byte(target.Port), 1)
	header = append(header, target.IP.To4()...)
	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var response [2]byte
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, nil
}

func dialLifecycleVMess(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	client, err := vmessref.NewClient(user.UUID, "aes-128-gcm", 0)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	conn, err := client.DialConn(raw, M.SocksaddrFromNet(target))
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

func dialLifecycleTrojan(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	header := []byte(trojanPasswordProof(user.UUID) + "\r\n")
	header = append(header, 1, 1)
	header = append(header, target.IP.To4()...)
	header = append(header, byte(target.Port>>8), byte(target.Port), '\r', '\n')
	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func dialLifecycleSS(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: user.UUID})
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return method.DialEarlyConn(raw, M.SocksaddrFromNet(target)), nil
}

// lifecycleEcho 是回显上游。holdAfterEOF 为真时读到 EOF 也不关（模拟不理会半关闭
// 的上游），用来验证单向收尾计时。active 是上游侧仍开着的连接数。
type lifecycleEcho struct {
	ln           net.Listener
	holdAfterEOF bool
	active       atomic.Int64
	mu           sync.Mutex
	conns        map[net.Conn]struct{}
	done         chan struct{}
}

func startLifecycleEcho(t testing.TB, holdAfterEOF bool) *lifecycleEcho {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &lifecycleEcho{ln: ln, holdAfterEOF: holdAfterEOF, conns: make(map[net.Conn]struct{}), done: make(chan struct{})}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			e.active.Add(1)
			e.mu.Lock()
			e.conns[conn] = struct{}{}
			e.mu.Unlock()
			go func() {
				defer func() {
					e.mu.Lock()
					delete(e.conns, conn)
					e.mu.Unlock()
					_ = conn.Close()
					e.active.Add(-1)
				}()
				buf := make([]byte, 4096)
				for {
					n, err := conn.Read(buf)
					if n > 0 {
						if _, werr := conn.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						if e.holdAfterEOF && err == io.EOF {
							// 不理会半关闭：一直开着，直到测试收尾。
							<-e.done
						}
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		close(e.done)
		e.mu.Lock()
		for c := range e.conns {
			_ = c.Close()
		}
		e.mu.Unlock()
	})
	return e
}

func (e *lifecycleEcho) addr() *net.TCPAddr { return e.ln.Addr().(*net.TCPAddr) }

// startLifecycleCore 起一个只有这一个入站的 NativeCore（直连出站），返回内核、端口、tag。
func startLifecycleCore(t testing.TB, p lifecycleProto, users []core.User) (*NativeCore, int, string) {
	t.Helper()
	allowLoopbackTargets(t)
	port := reserveTCPAndUDPPort(t)
	c := NewNativeCore(nil)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw := map[string]any{}
	for k, v := range p.raw {
		raw[k] = v
	}
	tag := "lc-" + p.name
	cfg := &core.InboundConfig{Tag: tag, Protocol: p.name, Listen: "127.0.0.1", Port: port, Raw: raw}
	if err := c.ApplyInbound(cfg, lifecycleAllowLoopback()); err != nil {
		t.Fatal(err)
	}
	if err := c.AddUsers(tag, users); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, port, tag
}

// reserveTCPAndUDPPort 找一个 TCP 与 UDP 此刻都空着的端口：同一个夹具既起 TCP 入站
// 也起 UDP / QUIC 入站，只按 TCP 预留时，号码可能正被别的测试的 UDP 客户端（临时
// 端口）占着，入站报「端口已被占用」。
func reserveTCPAndUDPPort(t testing.TB) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		tcp, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := tcp.Addr().(*net.TCPAddr).Port
		udp, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		_ = tcp.Close()
		if err != nil {
			continue
		}
		_ = udp.Close()
		return port
	}
	t.Fatal("找不到 TCP 与 UDP 都空着的端口")
	return 0
}

// liveSessions 是入站当前登记的在途会话数。
func (c *NativeCore) liveSessionsForTest(tag string) int {
	in, err := c.getInbound(tag)
	if err != nil {
		return 0
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	return liveSessionsOf(in.adapter)
}

// echoOnce 写一段数据并等回显，确认整条链路通了。
func echoOnce(conn net.Conn, payload string) error {
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte(payload)); err != nil {
		return err
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return err
	}
	if string(got) != payload {
		return fmt.Errorf("echo=%q", got)
	}
	return nil
}

// openFDs 是本进程打开的 fd 数（Linux 与 macOS 都有 /dev/fd）。
func openFDs() int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// waitFor 在 timeout 内轮询 cond，超时返回最后一次的说明。
func waitFor(timeout time.Duration, cond func() (bool, string)) (bool, string) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		ok, why := cond()
		if ok {
			return true, why
		}
		last = why
		if time.Now().After(deadline) {
			return false, last
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func goroutines() int { return runtime.NumGoroutine() }

// lifecycleAllowLoopback 是测试用的分流（直连）。上游回显就在本机，私网目标的
// 默认拒绝由 allowLoopbackTargets 在测试期间放开。
func lifecycleAllowLoopback() *core.Routing { return nil }

// allowLoopbackTargets 在本测试期间放开私网目标（outbound 默认拒绝回环）。
func allowLoopbackTargets(t testing.TB) {
	t.Helper()
	prev := outbound.BlockPrivateDestinations()
	outbound.SetBlockPrivateDestinations(false)
	t.Cleanup(func() { outbound.SetBlockPrivateDestinations(prev) })
}
