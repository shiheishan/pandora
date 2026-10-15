package kernel

// SS2022 UDP 多用户会话：互通、未知用户、超窗、重放与换地址、删人、名额、灌垃圾、空闲堆。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	ss2022ref "github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ss2022UDPTestClient 建一个经 SS2022 到适配器 UDP 端口的客户端；psks 为 {iPSK, uPSK}。
func ss2022UDPTestClient(t testing.TB, a *ss2022Adapter, method string, psks [][]byte, timeFunc func() time.Time) net.PacketConn {
	t.Helper()
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	methodObj, err := ss2022ref.New(method, psks, timeFunc)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return methodObj.DialPacketConn(raw)
}

// ss2022UDPRoundTrip 发一个包到 target 并在 timeout 内收回同样的负载。
func ss2022UDPRoundTrip(pc net.PacketConn, target net.Addr, payload string, timeout time.Duration) error {
	defer func() { _ = pc.SetReadDeadline(time.Time{}) }()
	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := pc.WriteTo([]byte(payload), target); err != nil {
			return err
		}
		_ = pc.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := pc.ReadFrom(buf)
		if err == nil {
			if string(buf[:n]) != payload {
				return fmt.Errorf("echo=%q", buf[:n])
			}
			return nil
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return err
		}
		lastErr = err
	}
	if lastErr != nil {
		return fmt.Errorf("%v 内无回显：%v", timeout, lastErr)
	}
	return fmt.Errorf("%v 内无回显", timeout)
}

// ss2022UDPSessionCount 是 SS2022 适配器当前的 UDP 会话数。
func ss2022UDPSessionCount(a *ss2022Adapter) int {
	a.udpMu.Lock()
	n := a.udpLive
	a.udpMu.Unlock()
	return n
}

// startSS2022UDPCore 经 NativeCore 起一个 SS2022 入站（真数据面），返回适配器。
func startSS2022UDPCore(t *testing.T, method string, users ...core.User) *ss2022Adapter {
	t.Helper()
	c, _, tag := startLifecycleCore(t, lifecycleProto{name: "shadowsocks", raw: map[string]any{
		"method":   method,
		"password": base64.StdEncoding.EncodeToString(ss2022TestIPSK(ss2022TestKeyLen(method))),
	}}, users)
	adapter := deviceWiringAdapter(t, c, tag)
	a, ok := adapter.(*ss2022Adapter)
	if !ok {
		t.Fatalf("adapter is %T, want *ss2022Adapter", adapter)
	}
	return a
}

// ss2022TCPUDPPlane 同时支持回环 TCP 拨号与 udpRelayTestPlane 的 UDP 出站。
type ss2022TCPUDPPlane struct {
	udpRelayTestPlane
}

func (*ss2022TCPUDPPlane) DialTCP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	return net.Dial("tcp", net.JoinHostPort(destination.Addr.String(), fmt.Sprint(destination.Port)))
}

// ss2022SwitchConn 测试用：Write 走当前选中的 UDP socket，Read 从两个 socket 合并。
type ss2022SwitchConn struct {
	mu   sync.Mutex
	cur  *net.UDPConn
	in   chan []byte
	done chan struct{}
}

func newSS2022SwitchConn(serverPort int) (*ss2022SwitchConn, *net.UDPConn, *net.UDPConn) {
	a, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: serverPort})
	if err != nil {
		panic(err)
	}
	b, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: serverPort})
	if err != nil {
		panic(err)
	}
	c := &ss2022SwitchConn{cur: a, in: make(chan []byte, 32), done: make(chan struct{})}
	readInto := func(conn *net.UDPConn) {
		buf := make([]byte, 64<<10)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, _, err := conn.ReadFrom(buf)
			if err != nil {
				select {
				case <-c.done:
					return
				default:
					continue
				}
			}
			pkt := append([]byte(nil), buf[:n]...)
			select {
			case c.in <- pkt:
			case <-c.done:
				return
			}
		}
	}
	go readInto(a)
	go readInto(b)
	return c, a, b
}

func (c *ss2022SwitchConn) switchTo(conn *net.UDPConn) {
	c.mu.Lock()
	c.cur = conn
	c.mu.Unlock()
}

func (c *ss2022SwitchConn) Read(p []byte) (int, error) {
	select {
	case b := <-c.in:
		if len(b) > len(p) {
			return 0, io.ErrShortBuffer
		}
		return copy(p, b), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *ss2022SwitchConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	conn := c.cur
	c.mu.Unlock()
	return conn.Write(p)
}

func (c *ss2022SwitchConn) Close() error {
	close(c.done)
	return nil
}

func (c *ss2022SwitchConn) LocalAddr() net.Addr                { return c.cur.LocalAddr() }
func (c *ss2022SwitchConn) RemoteAddr() net.Addr               { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (c *ss2022SwitchConn) SetDeadline(t time.Time) error      { return nil }
func (c *ss2022SwitchConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *ss2022SwitchConn) SetWriteDeadline(t time.Time) error { return nil }

func TestSS2022UDPMultiUserInterop(t *testing.T) {
	echo := hy2EchoUDP(t)
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"} {
		t.Run(method, func(t *testing.T) {
			u1 := core.User{ID: 8911, UUID: "ss2022-udp-user-a"}
			u2 := core.User{ID: 8912, UUID: "ss2022-udp-user-b"}
			a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, u1, u2)
			pc1 := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, u1.UUID), nil)
			pc2 := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, u2.UUID), nil)
			for i := 0; i < 5; i++ {
				p1 := fmt.Sprintf("u1-%d", i)
				p2 := fmt.Sprintf("u2-%d", i)
				if err := ss2022UDPRoundTrip(pc1, echo, p1, 3*time.Second); err != nil {
					t.Fatalf("u1 round %d: %v", i, err)
				}
				if err := ss2022UDPRoundTrip(pc2, echo, p2, 3*time.Second); err != nil {
					t.Fatalf("u2 round %d: %v", i, err)
				}
			}
			traffic, err := a.SnapshotTraffic()
			if err != nil {
				t.Fatal(err)
			}
			byID := map[int64]core.UserTraffic{}
			for _, tr := range traffic {
				byID[tr.ID] = tr
			}
			for _, id := range []int64{u1.ID, u2.ID} {
				tr := byID[id]
				if tr.Upload == 0 || tr.Download == 0 {
					t.Fatalf("traffic id=%d: %+v", id, tr)
				}
			}
			if cnt := ss2022UDPSessionCount(a); cnt != 2 {
				t.Fatalf("会话数=%d，期望 2", cnt)
			}
		})
	}
}

func TestSS2022UDPUnknownUserDropped(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	known := core.User{ID: 8921, UUID: "ss2022-udp-known"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, known)
	echo := hy2EchoUDP(t)
	unknownPSK := ss2022UserPSK("00000000-0000-4000-8000-000000000099", a.method.keyLen)
	pc := ss2022UDPTestClient(t, a, method, [][]byte{a.ipsk, unknownPSK}, nil)
	for i := 0; i < 3; i++ {
		_, _ = pc.WriteTo([]byte("probe"), echo)
	}
	_ = pc.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 256)
	if _, _, err := pc.ReadFrom(buf); err == nil {
		t.Fatal("未知用户应收不到回包")
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 0 {
		t.Fatalf("会话数=%d，期望 0", cnt)
	}
}

func TestSS2022UDPStaleTimestampDropped(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8922, UUID: "ss2022-udp-stale"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	echo := hy2EchoUDP(t)
	stale := func() time.Time { return time.Now().Add(-2 * time.Minute) }
	pc := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), stale)
	if err := ss2022UDPRoundTrip(pc, echo, "stale", time.Second); err == nil {
		t.Fatal("超窗时间戳应往返失败")
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 0 {
		t.Fatalf("会话数=%d，期望 0", cnt)
	}
}

func TestSS2022UDPReplayDropped(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8923, UUID: "ss2022-udp-replay"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	u := ss2022BenchUser(t, a, user.UUID)
	sink, recvCount := startSSCountingSink(t)
	sinkAP := sink.AddrPort()
	sid, pid := uint64(0xaabbccddeeff0011), uint64(42)
	pkt := ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, pid, time.Now(), sinkAP, []byte("replay-me"))
	pkt = append([]byte(nil), pkt...)

	rawA, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawA.Close() })
	rawB, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawB.Close() })

	if _, err := rawA.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 1, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("首包未到 sink：%s", why)
	}
	key := ssUDPKey{sid: sid}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		s := a.findUDPSession(key, u)
		return s != nil && s.ready.Load(), "session not ready"
	}); !ok {
		t.Fatalf("会话未就绪：%s", why)
	}
	s := a.findUDPSession(key, u)
	addrA := s.clientAddr()

	if _, err := rawA.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if _, err := rawB.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return a.udpDrops.replay.Load() == 2, fmt.Sprintf("replay=%d", a.udpDrops.replay.Load())
	}); !ok {
		t.Fatalf("重放计数：%s", why)
	}
	time.Sleep(200 * time.Millisecond)
	if recvCount() != 1 {
		t.Fatalf("重放后 sink=%d，期望 1", recvCount())
	}
	if ss2022UDPSessionCount(a) != 1 {
		t.Fatalf("会话数=%d，期望 1", ss2022UDPSessionCount(a))
	}
	if s.clientAddr() != addrA {
		t.Fatalf("重放后 clientAddr=%v，期望仍为 A %v", s.clientAddr(), addrA)
	}

	pkt2 := ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, pid+1, time.Now(), sinkAP, []byte("replay-me"))
	if _, err := rawB.Write(pkt2); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 2, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("新 pid 包未到 sink：%s", why)
	}
	localB := rawB.LocalAddr().(*net.UDPAddr).AddrPort()
	wantB := netip.AddrPortFrom(localB.Addr().Unmap(), localB.Port())
	if s.clientAddr() != wantB {
		t.Fatalf("roam 后 clientAddr=%v，期望 B %v", s.clientAddr(), wantB)
	}
}

func TestSS2022UDPRoaming(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8924, UUID: "ss2022-udp-roam", DeviceLimit: 1}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	echo := hy2EchoUDP(t)
	switchConn, sockA, sockB := newSS2022SwitchConn(a.spec.Config.Port)
	t.Cleanup(func() {
		_ = switchConn.Close()
		_ = sockA.Close()
		_ = sockB.Close()
	})
	methodObj, err := ss2022ref.New(method, ss2022TestClientPSKs(a, user.UUID), nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := methodObj.DialPacketConn(switchConn)
	if err := ss2022UDPRoundTrip(pc, echo, "roam-1", 3*time.Second); err != nil {
		t.Fatalf("第一次往返：%v", err)
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 1 {
		t.Fatalf("会话数=%d，期望 1", cnt)
	}
	if got := a.OnlineIPs()[user.ID]; !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("在线=%v，期望 [127.0.0.1]", got)
	}
	switchConn.switchTo(sockB)
	if err := ss2022UDPRoundTrip(pc, echo, "roam-2", 3*time.Second); err != nil {
		t.Fatalf("换 socket 后往返：%v", err)
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 1 {
		t.Fatalf("换址后会话数=%d，期望 1", cnt)
	}
	if got := a.OnlineIPs()[user.ID]; !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("换址后在线=%v", got)
	}
}

func TestSS2022UDPDelUserClosesSessions(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	u1 := core.User{ID: 8925, UUID: "ss2022-udp-del-a"}
	a := startSS2022Adapter(t, method, &ss2022TCPUDPPlane{}, nil, u1)
	echoUDP := hy2EchoUDP(t)
	tcpEcho := startLifecycleEcho(t, false)
	payload := "ss2022-del-payload"

	pc := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, u1.UUID), nil)
	if err := ss2022UDPRoundTrip(pc, echoUDP, payload, 2*time.Second); err != nil {
		t.Fatalf("UDP 往返：%v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	tcpConn := ss2022DialClient(t, method, ss2022TestClientPSKs(a, u1.UUID), addr, M.SocksaddrFromNet(tcpEcho.addr()), nil)
	t.Cleanup(func() { _ = tcpConn.Close() })
	if _, err := tcpConn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if _, err := io.ReadFull(tcpConn, buf[:len(payload)]); err != nil {
		t.Fatalf("TCP 往返：%v", err)
	}
	if ss2022UDPSessionCount(a) != 1 {
		t.Fatalf("删人前会话数=%d，期望 1", ss2022UDPSessionCount(a))
	}
	// 用户会话表里登记了一个 UDP 会话与一条 TCP 连接。
	if liveSessionsOf(a) != 2 {
		t.Fatalf("删人前 live=%d，期望 2（UDP 会话 + TCP 连接）", liveSessionsOf(a))
	}

	if err := a.DelUsers([]string{u1.UUID}); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		return ss2022UDPSessionCount(a) == 0 && liveSessionsOf(a) == 0,
			fmt.Sprintf("udp=%d live=%d", ss2022UDPSessionCount(a), liveSessionsOf(a))
	}); !ok {
		t.Fatalf("删用户后未收尾：%s", why)
	}
	_ = tcpConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := tcpConn.Read(buf); err == nil {
		t.Fatal("删用户后 TCP 应读到错误")
	}
	if err := ss2022UDPRoundTrip(pc, echoUDP, payload, 500*time.Millisecond); err == nil {
		t.Fatal("删用户后 UDP 应往返失败")
	}
}

func TestSS2022UDPSessionQuota(t *testing.T) {
	// 空闲时限要明显长于第三个客户端的等待（500ms）：原先 300ms 时客户端 1 的会话在第三个客户端重试期间
	// 就被空闲回收、名额归还，第三个客户端因此建成了会话（用例构造错，不是名额没生效）。
	setSSUDPIdle(t, 2*time.Second)

	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8926, UUID: "ss2022-udp-quota"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, func(ad *ss2022Adapter) {
		ad.udpQuota.mu.Lock()
		ad.udpQuota.limit = 2
		ad.udpQuota.mu.Unlock()
	}, user)
	echo := hy2EchoUDP(t)
	pc1 := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), nil)
	pc2 := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), nil)
	pc3 := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), nil)
	payload := "ss2022-quota"

	if err := ss2022UDPRoundTrip(pc1, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("客户端 1：%v", err)
	}
	if err := ss2022UDPRoundTrip(pc2, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("客户端 2：%v", err)
	}
	if err := ss2022UDPRoundTrip(pc3, echo, payload, 500*time.Millisecond); err == nil {
		t.Fatal("第三个客户端应被拒")
	}
	if ss2022UDPSessionCount(a) != 2 {
		t.Fatalf("会话数=%d，期望 2", ss2022UDPSessionCount(a))
	}

	if ok, why := waitFor(8*time.Second, func() (bool, string) {
		a.udpQuota.mu.Lock()
		inUse := a.udpQuota.byUser[user.ID]
		a.udpQuota.mu.Unlock()
		return ss2022UDPSessionCount(a) == 0 && inUse == 0, fmt.Sprintf("会话=%d 名额=%d", ss2022UDPSessionCount(a), inUse)
	}); !ok {
		t.Fatalf("空闲后未回收：%s", why)
	}
	if err := ss2022UDPRoundTrip(pc3, echo, payload, 2*time.Second); err != nil {
		t.Fatalf("名额腾出后客户端 3：%v", err)
	}
}

func TestSS2022UDPGarbageFloodKeepsEstablishedSessions(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8927, UUID: "ss2022-udp-flood-owner"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	fillerUsers := make([]core.User, 200)
	for i := 0; i < 200; i++ {
		fillerUsers[i] = core.User{
			ID:   int64(91000 + i),
			UUID: fmt.Sprintf("ss2022-udp-filler-%d", i),
		}
	}
	if err := a.AddUsers(fillerUsers); err != nil {
		t.Fatal(err)
	}
	echo := hy2EchoUDP(t)
	pc := ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), nil)
	if err := ss2022UDPRoundTrip(pc, echo, "flood-warmup", 3*time.Second); err != nil {
		t.Fatalf("warmup：%v", err)
	}

	stop := make(chan struct{})
	var floodWG sync.WaitGroup
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port}
	var floodConns []*net.UDPConn
	for i := 0; i < 2; i++ {
		conn, err := net.DialUDP("udp4", nil, serverAddr)
		if err != nil {
			t.Fatal(err)
		}
		floodConns = append(floodConns, conn)
		floodWG.Add(1)
		go func(c *net.UDPConn) {
			defer floodWG.Done()
			garbage := make([]byte, 1200)
			_, _ = rand.Read(garbage)
			var counter uint64
			for {
				select {
				case <-stop:
					return
				default:
				}
				for j := 0; j < 32; j++ {
					counter++
					binary.LittleEndian.PutUint64(garbage[:8], counter)
					if _, err := c.Write(garbage); err != nil {
						return
					}
				}
				time.Sleep(time.Millisecond)
			}
		}(conn)
	}
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			close(stop)
			floodWG.Wait()
			for _, c := range floodConns {
				_ = c.Close()
			}
		})
	})
	time.Sleep(200 * time.Millisecond)
	for i := 0; i < 10; i++ {
		if err := ss2022UDPRoundTrip(pc, echo, fmt.Sprintf("flood-%d", i), 3*time.Second); err != nil {
			t.Fatalf("灌包期间往返失败 (第 %d 次): %v", i, err)
		}
	}
	t.Logf("快路径队列丢包 a.udpDrops.queue: %d", a.udpDrops.queue.Load())
}

func TestSS2022IdleUDPSessionHeap(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器自带的分配会抬高堆，不量")
	}
	const sessions = 1000
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8928, UUID: "ss2022-udp-mem"}
	// 走真 NativeCore 的数据面（同经典 TestShadowsocksIdleUDPSessionHeap 的 startSSUDPCore）：直连出站的
	// 下行就绪了才借收包缓冲；udpRelayTestPlane 只给 net.PacketConn，下行只能常驻 64KB 阻塞读，量的就不是空闲会话了。
	a := startSS2022UDPCore(t, method, user)
	a.udpQuota.mu.Lock()
	a.udpQuota.limit = 2000
	a.udpQuota.mu.Unlock()
	echo := hy2EchoUDP(t)
	clients := make([]net.PacketConn, sessions)
	for i := range clients {
		clients[i] = ss2022UDPTestClient(t, a, method, ss2022TestClientPSKs(a, user.UUID), nil)
	}
	before := heapAfterGC()
	buf := make([]byte, 2048)
	for i, pc := range clients {
		if _, err := pc.WriteTo([]byte("ping"), echo); err != nil {
			t.Fatalf("client %d write: %v", i, err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("client %d echo: n=%d err=%v", i, n, err)
		}
	}
	if ok, why := waitFor(5*time.Second, func() (bool, string) {
		cnt := ss2022UDPSessionCount(a)
		return cnt == sessions, fmt.Sprintf("count=%d want %d", cnt, sessions)
	}); !ok {
		t.Fatalf("等待会话建齐: %s", why)
	}
	after := heapAfterGC()
	perSession := (int64(after) - int64(before)) / sessions
	t.Logf("%d 个空闲会话：存活堆 %d → %d，平均每会话 %d 字节", sessions, before, after, perSession)
	if os.Getenv("PDND_SS_MEM_REPORT") != "" {
		return
	}
	if perSession > 16<<10 {
		t.Fatalf("每个空闲会话常驻 %d 字节，超过 16KB 上限", perSession)
	}
}

// 别的节点的 iPSK 配上正确的 uPSK：认不出用户，快路径丢包，不回包、不建会话。
func TestSS2022UDPOtherNodeIPSKDropped(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8929, UUID: "ss2022-udp-other-node"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	echo := hy2EchoUDP(t)
	otherIPSK := make([]byte, 16)
	for i := range otherIPSK {
		otherIPSK[i] = 0x5a
	}
	pc := ss2022UDPTestClient(t, a, method, [][]byte{otherIPSK, ss2022UserPSK(user.UUID, a.method.keyLen)}, nil)
	if err := ss2022UDPRoundTrip(pc, echo, "other-node", time.Second); err == nil {
		t.Fatal("别的节点的 iPSK 应收不到回包")
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 0 {
		t.Fatalf("会话数=%d，期望 0", cnt)
	}
}

// 篡改身份头（EIH）一个字节：丢包、不转发、不建会话；同一个包不改再发被转发（对照）。
func TestSS2022UDPTamperedEIHDropped(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	user := core.User{ID: 8930, UUID: "ss2022-udp-tampered-eih"}
	a := startSS2022Adapter(t, method, &udpRelayTestPlane{}, nil, user)
	u := ss2022BenchUser(t, a, user.UUID)
	sink, recvCount := startSSCountingSink(t)
	pkt := ss2022TestClientPacket(a.ipsk, u.MasterKey, 0x0102030405060708, 1, time.Now(), sink.AddrPort(), []byte("eih-probe"))
	tampered := append([]byte(nil), pkt...)
	tampered[16] ^= 0x01 // 分离头 16 字节之后就是身份头
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	for i := 0; i < 3; i++ {
		if _, err := raw.Write(tampered); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if n := recvCount(); n != 0 {
		t.Fatalf("篡改身份头的包被转发了 %d 个", n)
	}
	if cnt := ss2022UDPSessionCount(a); cnt != 0 {
		t.Fatalf("会话数=%d，期望 0", cnt)
	}
	if _, err := raw.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 1, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("未篡改的包应被转发：%s", why)
	}
}
