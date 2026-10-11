package kernel

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	SS "github.com/sagernet/sing-shadowsocks2"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

// 认证失败后的排空（drainUntilPeerClose）：不设超时、一直读到对端关，关闭
// 时机与探测内容、长度无关（NDSS'20 Frolov 等：最常见的「不回数据」行为就是
// 永不超时）。兜底只有两条：读满 drainMaxBytes 即关；全进程同时排空的连接数
// 到 drainMaxConcurrent 时，新的失败连接退回立即关。

// 排空测试用的读请求头截止：比它晚很多仍未关，才说明不再按截止关。
const drainTestHeaderTimeout = 200 * time.Millisecond

// probeUntilPeerClose 发 payload，等过读请求头截止 past 之后确认服务端仍在
// 读、没有关；再由客户端半关，服务端应随即以 FIN（不是 RST）关闭、不回任何
// 字节。
func probeUntilPeerClose(t *testing.T, addr string, payload []byte, past time.Duration) {
	t.Helper()
	conn := dialDrainProbe(t, addr, payload)
	defer conn.Close()
	time.Sleep(past)
	if _, err := conn.Write(make([]byte, 64)); err != nil {
		t.Fatalf("过了读请求头截止仍应能写：%v", err)
	}
	assertStillOpen(t, conn)
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("对端半关后服务端应以 FIN 关闭，实际 err=%v（%v）", err, time.Since(start))
	}
	if len(got) != 0 {
		t.Fatalf("认证失败不应回任何字节，收到 %d 字节", len(got))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("对端半关后 %v 才关", elapsed)
	}
}

func dialDrainProbe(t *testing.T, addr string, payload []byte) *net.TCPConn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn := raw.(*net.TCPConn)
	if len(payload) > 0 {
		if _, err := conn.Write(payload); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
	}
	return conn
}

// assertStillOpen：短读截止内既读不到数据也读不到 EOF / RST。
func assertStillOpen(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var one [1]byte
	n, err := conn.Read(one[:])
	_ = conn.SetReadDeadline(time.Time{})
	if n != 0 || !isTimeoutErr(err) {
		t.Fatalf("服务端应仍在读、未关闭，实际 n=%d err=%v", n, err)
	}
}

// assertClosedPromptly：客户端不半关，服务端也在 within 内关掉（EOF 或 RST）。
func assertClosedPromptly(t *testing.T, conn net.Conn, within time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	got, err := io.ReadAll(conn)
	if isTimeoutErr(err) {
		t.Fatalf("%v 内服务端未关闭", within)
	}
	if len(got) != 0 {
		t.Fatalf("不应回任何字节，收到 %d 字节", len(got))
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestShadowsocksAuthFailureDrainsUntilPeerClose(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	probeUntilPeerClose(t, addr, randomBytes(50), 3*drainTestHeaderTimeout)
}

// 不足一个请求头的短探测：先撞读请求头截止，之后与认证失败同样一直读。
func TestShadowsocksShortProbeDrainsUntilPeerClose(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	probeUntilPeerClose(t, addr, randomBytes(5), 3*drainTestHeaderTimeout)
}

// 只建连不发：同样不按截止关。
func TestShadowsocksSilentProbeDrainsUntilPeerClose(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	probeUntilPeerClose(t, addr, nil, 3*drainTestHeaderTimeout)
}

// 把一条合法连接的首包原样重放：salt 相同，第二次必须被拦下、同样一直读。
func TestShadowsocksReplayedSaltDrainsUntilPeerClose(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
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
	probeUntilPeerClose(t, addr, recorder.bytes(), 3*drainTestHeaderTimeout)
}

func TestVMessAuthFailureDrainsUntilPeerClose(t *testing.T) {
	addr, _ := newTestVMess(t, drainTestHeaderTimeout)
	probeUntilPeerClose(t, addr, randomBytes(50), 3*drainTestHeaderTimeout)
}

func TestVMessShortProbeDrainsUntilPeerClose(t *testing.T) {
	addr, _ := newTestVMess(t, drainTestHeaderTimeout)
	probeUntilPeerClose(t, addr, randomBytes(5), 3*drainTestHeaderTimeout)
}

func TestVMessReplayedHeaderDrainsUntilPeerClose(t *testing.T) {
	addr, id := newTestVMess(t, drainTestHeaderTimeout)
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
	probeUntilPeerClose(t, addr, recorder.bytes(), 3*drainTestHeaderTimeout)
}

// setDrainMaxConcurrent 临时改全进程排空并发上限，测试结束还原。
func setDrainMaxConcurrent(t *testing.T, n int64) {
	t.Helper()
	old := drainMaxConcurrent.Load()
	drainMaxConcurrent.Store(n)
	t.Cleanup(func() { drainMaxConcurrent.Store(old) })
}

func waitDrainActive(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for drainActive.Load() != want {
		if time.Now().After(deadline) {
			t.Fatalf("排空中的连接数 %d，等不到 %d", drainActive.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func setDrainLimit(t *testing.T, v *atomic.Int64, n int64) {
	t.Helper()
	old := v.Load()
	v.Store(n)
	t.Cleanup(func() { v.Store(old) })
}

// assertFIN：服务端在 within 内以 FIN 关闭（读到 EOF，不是 RST），且不回字节。
func assertFIN(t *testing.T, conn net.Conn, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(within))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("服务端应以 FIN 关闭，实际 err=%v（%v）", err, time.Since(start))
	}
	if len(got) != 0 {
		t.Fatalf("不应回任何字节，收到 %d 字节", len(got))
	}
	return time.Since(start)
}

// 名额满时踢最老的、放新的进来（review-r3 #1）：未认证的一方开连接各发几十字节
// 就挂着，以前能把名额永久占满，之后的认证失败退回立即关，接收缓冲里有没读的
// 字节，发的是 RST——正是排空要消掉的特征。现在新探测照样排空、读到对端关才
// FIN；被踢的最老一条早已读空，收到的是 FIN；名额计数始终等于上限。
func TestDrainFullEvictsOldestWithFIN(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	waitDrainActive(t, 0)
	setDrainMaxConcurrent(t, 2)
	setDrainLimit(t, &drainMaxPerSource, 100)
	oldest := dialDrainProbe(t, addr, randomBytes(50))
	defer oldest.Close()
	waitDrainActive(t, 1)
	second := dialDrainProbe(t, addr, randomBytes(50))
	defer second.Close()
	waitDrainActive(t, 2)

	// 名额已满：新探测仍排空（过了读请求头截止也不关，客户端半关后 FIN）。
	probe := dialDrainProbe(t, addr, randomBytes(50))
	defer probe.Close()
	assertFIN(t, oldest, 2*time.Second)
	waitDrainActive(t, 2)
	time.Sleep(2 * drainTestHeaderTimeout)
	assertStillOpen(t, second)
	assertStillOpen(t, probe)
	if err := probe.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	assertFIN(t, probe, 2*time.Second)
	waitDrainActive(t, 1)
}

// 同一来源排空的连接数有上限（review-r3 #1）：超出时踢同一来源最老的一条
// （FIN），一个来源占不满全进程的名额。
func TestDrainPerSourceCap(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	waitDrainActive(t, 0)
	setDrainMaxConcurrent(t, 100)
	setDrainLimit(t, &drainMaxPerSource, 2)
	conns := make([]*net.TCPConn, 0, 5)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		conns = append(conns, dialDrainProbe(t, addr, randomBytes(50)))
		waitDrainActive(t, int64(min(i+1, 2)))
	}
	for _, c := range conns[:3] {
		assertFIN(t, c, 2*time.Second)
	}
	waitDrainActive(t, 2)
	assertStillOpen(t, conns[3])
	assertStillOpen(t, conns[4])
}

// 每条排空另有与对端行为无关的总时限（review-r3 #1）：对端内核活着就会回应
// keepalive，挂着不发 FIN 的连接以前永远不走。到时限读空后 FIN。
func TestDrainMaxDurationClosesWithFIN(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	waitDrainActive(t, 0)
	setDrainLimit(t, &drainMaxDuration, int64(400*time.Millisecond))
	setDrainLimit(t, &drainJitter, 0)
	for _, payload := range [][]byte{nil, randomBytes(50)} {
		conn := dialDrainProbe(t, addr, payload)
		elapsed := assertFIN(t, conn, 3*time.Second)
		_ = conn.Close()
		if elapsed < 300*time.Millisecond {
			t.Fatalf("排空 %v 就关了，早于总时限", elapsed)
		}
	}
	waitDrainActive(t, 0)
}

// 并发登记、踢出、注销下名额计数不漂（-race）：大量探测同时到达，计数封顶在上限，
// 全部关掉后回零。
func TestDrainRegistryCountUnderChurn(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	waitDrainActive(t, 0)
	setDrainMaxConcurrent(t, 8)
	setDrainLimit(t, &drainMaxPerSource, 100)
	const n = 64
	conns := make(chan *net.TCPConn, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				return
			}
			_, _ = c.Write(randomBytes(50))
			conns <- c.(*net.TCPConn)
		}()
	}
	wg.Wait()
	close(conns)
	time.Sleep(3 * drainTestHeaderTimeout)
	if got := drainActive.Load(); got != 8 {
		t.Fatalf("名额计数 %d，期望封顶 8", got)
	}
	for c := range conns {
		_ = c.Close()
	}
	waitDrainActive(t, 0)
	drains.mu.Lock()
	all, sources := drains.all.Len(), len(drains.bySource)
	drains.mu.Unlock()
	if all != 0 || sources != 0 {
		t.Fatalf("名册残留 %d 条、%d 个来源", all, sources)
	}
}

func TestDrainSourceKey(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 1}, "203.0.113.7"},
		{&net.TCPAddr{IP: net.ParseIP("::ffff:203.0.113.7"), Port: 1}, "203.0.113.7"},
		{&net.TCPAddr{IP: net.ParseIP("2001:db8:1:2:3:4:5:6"), Port: 1}, "2001:db8:1:2::/64"},
		{nil, ""},
	} {
		if got := drainSource(tc.addr); got != tc.want {
			t.Errorf("drainSource(%v) = %q，期望 %q", tc.addr, got, tc.want)
		}
	}
}

// 读满 drainMaxBytes 即关：不给探测方当黑洞带宽用。
func TestDrainStopsAtByteCap(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	conn := dialDrainProbe(t, addr, randomBytes(50))
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	chunk := make([]byte, 256<<10)
	var sent int64
	var writeErr error
	for sent < 4*drainMaxBytes {
		n, err := conn.Write(chunk)
		sent += int64(n)
		if err != nil {
			writeErr = err
			break
		}
	}
	if writeErr == nil || isTimeoutErr(writeErr) {
		t.Fatalf("写了 %d 字节服务端仍在收，err=%v", sent, writeErr)
	}
	if !errors.Is(writeErr, syscall.EPIPE) && !errors.Is(writeErr, syscall.ECONNRESET) {
		t.Logf("写端错误 %v（期望 EPIPE / ECONNRESET）", writeErr)
	}
	if sent < drainMaxBytes {
		t.Fatalf("只写了 %d 字节就被关，早于上限 %d", sent, int64(drainMaxBytes))
	}
}

// 每条排空连接的常驻内存（堆 + goroutine 栈，含客户端一侧的 conn 对象），
// drainMaxConcurrent 乘以它就是这条路径的内存上限。上限 64KB 只挡回归。
func TestDrainMemoryPerConn(t *testing.T) {
	_, addr := startTestShadowsocks(t, drainTestHeaderTimeout)
	const n = 400
	// 测试连接都来自 127.0.0.1，放开同源上限才能量 400 条。
	setDrainLimit(t, &drainMaxPerSource, n)
	base := drainActive.Load()
	measure := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse + ms.StackInuse
	}
	before := measure()
	conns := make([]*net.TCPConn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, dialDrainProbe(t, addr, randomBytes(50)))
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	waitDrainActive(t, base+n)
	after := measure()
	per := int64(after-before) / n
	t.Logf("每条排空连接常驻约 %d 字节（%d 条，堆+栈增量 %d 字节）", per, n, int64(after-before))
	if per > 64<<10 {
		t.Fatalf("每条排空连接常驻 %d 字节，超过 64KB", per)
	}
}

// 认证失败先上报、再排空：不必等对端关，日志里就能看到认证失败。
func TestDrainReportsAuthFailureBeforePeerClose(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		raw      map[string]any
	}{
		{"shadowsocks", map[string]any{"method": "aes-128-gcm"}},
		{"vmess", nil},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			port := reserveTCPPort(t)
			_, rec := startHookedAdapter(t, tc.protocol, port, tc.raw, refusePlane{}, hookUser)
			conn := dialDrainProbe(t, net.JoinHostPort("127.0.0.1", itoa(port)), randomBytes(64))
			defer conn.Close()
			rec.wait(t, tc.protocol, StageSession, connErrAuth)
			assertStillOpen(t, conn)
		})
	}
}
