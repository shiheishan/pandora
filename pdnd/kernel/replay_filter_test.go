package kernel

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	SS "github.com/sagernet/sing-shadowsocks2"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

func TestReplayFilterRetentionWindow(t *testing.T) {
	f := newReplayFilter(time.Minute, 3, 1000)
	base := time.Unix(1_700_000_000, 0)
	key := []byte("salt-0001")
	if !f.check(key, base) {
		t.Fatal("首次出现应放行")
	}
	// 保留期下限是 (keep-1)*period = 2 分钟：2 分钟内任何时刻重放都要拦住。
	for _, offset := range []time.Duration{time.Second, 59 * time.Second, 61 * time.Second, 119 * time.Second} {
		if f.check(key, base.Add(offset)) {
			t.Fatalf("%v 后的重放被放行", offset)
		}
	}
	// 上限是 keep*period = 3 分钟：之后整代丢弃，同一个键重新放行。
	if !f.check(key, base.Add(3*time.Minute+time.Second)) {
		t.Fatal("超过保留期上限的键仍未清掉")
	}
	// 时间跳很远：所有代一次清空，不逐代空转。
	if !f.check([]byte("other"), base.Add(24*time.Hour)) || f.size() != 1 {
		t.Fatalf("长时间跳变后应只剩 1 条，实际 %d", f.size())
	}
}

func TestReplayFilterMemoryIsBounded(t *testing.T) {
	const perGen, keep = 128, 3
	f := newReplayFilter(time.Minute, keep, perGen)
	now := time.Unix(1_700_000_000, 0)
	var key [8]byte
	for i := 0; i < 100*perGen; i++ {
		binary.BigEndian.PutUint64(key[:], uint64(i))
		if !f.check(key[:], now) {
			t.Fatalf("第 %d 个新键被当成重放", i)
		}
	}
	if got := f.size(); got > perGen*keep {
		t.Fatalf("条目数 %d 超过上限 %d", got, perGen*keep)
	}
	// 洪泛下最近写入的那一代仍在。
	binary.BigEndian.PutUint64(key[:], uint64(100*perGen-1))
	if f.check(key[:], now) {
		t.Fatal("最近写入的键应仍被拦住")
	}
}

// 测试用的多连接回显上游。
func startMultiEcho(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

// recordingConn 记下客户端写出的全部字节，用来原样重放。
type recordingConn struct {
	net.Conn
	mu      sync.Mutex
	written []byte
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.written = append(c.written, p...)
	c.mu.Unlock()
	return c.Conn.Write(p)
}

func (c *recordingConn) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.written...)
}

// probeDrain 发 payload 后看服务端的反应：收到多少字节、多久后关。中途
// 再写一次，确认服务端还在读（没有提前关）。
func probeDrain(t *testing.T, addr string, payload []byte, midWriteAfter time.Duration) (int, time.Duration) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(midWriteAfter)
	if _, err := conn.Write(make([]byte, 64)); err != nil {
		t.Fatalf("认证失败后服务端应继续读，第二次写失败：%v", err)
	}
	got, err := io.ReadAll(conn)
	if isTimeoutErr(err) {
		t.Fatalf("服务端到截止时间仍未关闭连接")
	}
	return len(got), time.Since(start)
}

func startTestShadowsocks(t *testing.T, headerTimeout time.Duration) (*shadowsocksAdapter, string) {
	t.Helper()
	port := reserveTCPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"method": "aes-128-gcm"}}}
	value, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := value.(*shadowsocksAdapter)
	adapter.headerTimeout = headerTimeout
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(startMultiEcho(t)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.AddUsers([]core.User{{ID: 77, UUID: "replay-ss-password"}}); err != nil {
		t.Fatal(err)
	}
	return adapter, fmt.Sprintf("127.0.0.1:%d", port)
}

// Shadowsocks 认证失败：以前读完 salt+18 字节就 RST，现在收下数据、不回
// 字节、到读请求头的截止时间才关。
func TestShadowsocksAuthFailureDrainsUntilDeadline(t *testing.T) {
	const timeout = 400 * time.Millisecond
	_, addr := startTestShadowsocks(t, timeout)
	junk := make([]byte, 50)
	_, _ = rand.Read(junk)
	n, elapsed := probeDrain(t, addr, junk, 100*time.Millisecond)
	if n != 0 || elapsed < timeout-50*time.Millisecond || elapsed > timeout+time.Second {
		t.Fatalf("收到 %d 字节、%v 后关闭；期望 0 字节、约 %v", n, elapsed, timeout)
	}
}

// 把一条合法连接的首包原样重放：salt 相同，第二次必须被拦下且同样读到超时。
func TestShadowsocksRejectsReplayedSalt(t *testing.T) {
	const timeout = 400 * time.Millisecond
	_, addr := startTestShadowsocks(t, timeout)
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingConn{Conn: raw}
	defer recorder.Close()
	_ = recorder.SetDeadline(time.Now().Add(3 * time.Second))
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: "replay-ss-password"})
	if err != nil {
		t.Fatal(err)
	}
	stream := method.DialEarlyConn(recorder, M.ParseSocksaddrHostPort("127.0.0.1", 443))
	if _, err := stream.Write([]byte("first-flight")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("first-flight"))
	if _, err := io.ReadFull(stream, echo); err != nil || string(echo) != "first-flight" {
		t.Fatalf("合法连接 echo=%q err=%v", echo, err)
	}
	n, elapsed := probeDrain(t, addr, recorder.bytes(), 50*time.Millisecond)
	if n != 0 || elapsed < timeout-50*time.Millisecond {
		t.Fatalf("重放首包：收到 %d 字节、%v 后关闭；期望被拦下并读到超时", n, elapsed)
	}
}

func newTestVMess(t *testing.T, headerTimeout time.Duration) (string, string) {
	t.Helper()
	port := reserveTCPPort(t)
	id := uuid.New().String()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{}), headerTimeout: headerTimeout}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsers([]core.User{{ID: 78, UUID: id}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(startMultiEcho(t)).Unwrap()}
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return fmt.Sprintf("127.0.0.1:%d", port), id
}

// VMess authID 对不上：以前读 16 字节就断，现在读到截止时间。
func TestVMessAuthFailureDrainsUntilDeadline(t *testing.T) {
	const timeout = 400 * time.Millisecond
	addr, _ := newTestVMess(t, timeout)
	junk := make([]byte, 50)
	_, _ = rand.Read(junk)
	n, elapsed := probeDrain(t, addr, junk, 100*time.Millisecond)
	if n != 0 || elapsed < timeout-50*time.Millisecond || elapsed > timeout+time.Second {
		t.Fatalf("收到 %d 字节、%v 后关闭；期望 0 字节、约 %v", n, elapsed, timeout)
	}
}

func TestVMessReplayedHeaderDrainsUntilDeadline(t *testing.T) {
	const timeout = 400 * time.Millisecond
	addr, id := newTestVMess(t, timeout)
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingConn{Conn: raw}
	defer recorder.Close()
	client, err := vmessref.NewClient(id, "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialConn(recorder, M.ParseSocksaddrHostPort("127.0.0.1", 443))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("first-flight")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("first-flight"))
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "first-flight" {
		t.Fatalf("合法连接 echo=%q err=%v", echo, err)
	}
	n, elapsed := probeDrain(t, addr, recorder.bytes(), 50*time.Millisecond)
	if n != 0 || elapsed < timeout-50*time.Millisecond {
		t.Fatalf("重放请求头：收到 %d 字节、%v 后关闭；期望被拦下并读到超时", n, elapsed)
	}
}

// legacyVMessReplay 是改动前 acceptAuthID 的原样实现，只留给基准对照：每个
// 连接在锁内把整张表扫一遍清过期项。
type legacyVMessReplay struct {
	mu     sync.Mutex
	replay map[string]time.Time
}

func (l *legacyVMessReplay) accept(id [16]byte) bool {
	now := time.Now()
	key := string(id[:])
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.replay == nil {
		l.replay = make(map[string]time.Time)
	}
	for k, seen := range l.replay {
		if now.Sub(seen) > 2*time.Minute {
			delete(l.replay, k)
		}
	}
	if _, exists := l.replay[key]; exists {
		return false
	}
	l.replay[key] = now
	return true
}

// 1 万条在册记录下，每来一个新连接做一次重放检查的开销。
// go test ./kernel -run '^$' -bench 'VMessReplay' -benchmem
func BenchmarkVMessReplay10k(b *testing.B) {
	const prefill = 10_000
	ids := func(n int) [][16]byte {
		out := make([][16]byte, n)
		for i := range out {
			_, _ = rand.Read(out[i][:])
		}
		return out
	}
	b.Run("legacy-full-scan", func(b *testing.B) {
		l := &legacyVMessReplay{}
		for _, id := range ids(prefill) {
			l.accept(id)
		}
		fresh := ids(b.N)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			l.accept(fresh[i])
			// 保持表里恰好 1 万条，量的是「1 万条时」的单次开销。
			l.mu.Lock()
			delete(l.replay, string(fresh[i][:]))
			l.mu.Unlock()
		}
	})
	b.Run("bucketed", func(b *testing.B) {
		a := &vmessAdapter{}
		for _, id := range ids(prefill) {
			a.acceptAuthID(id)
		}
		fresh := ids(b.N)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			a.acceptAuthID(fresh[i])
		}
	})
}
