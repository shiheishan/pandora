package kernel

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	quic "github.com/apernet/quic-go"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

func readJuicityPacket(r io.Reader) (juicityAddress, []byte, error) {
	target, err := readJuicityAddress(r)
	if err != nil {
		return juicityAddress{}, nil, err
	}
	var length [2]byte
	if _, err := io.ReadFull(r, length[:]); err != nil {
		return juicityAddress{}, nil, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > juicityMaxPacket {
		return juicityAddress{}, nil, fmt.Errorf("juicity packet length is invalid")
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return juicityAddress{}, nil, err
	}
	return target, payload, nil
}

func marshalJuicityPacket(target juicityAddress, payload []byte) []byte {
	address := marshalJuicityAddress(target)
	frame := make([]byte, len(address)+2+len(payload))
	copy(frame, address)
	binary.BigEndian.PutUint16(frame[len(address):], uint16(len(payload)))
	copy(frame[len(address)+2:], payload)
	return frame
}

// startJuicityLoopback 起一个回显数据面的 Juicity 入站，返回已认证的 QUIC 客户端。
func startJuicityLoopback(t *testing.T, userID string, id int64) (*juicityAdapter, *quic.Conn) {
	t.Helper()
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "juicity", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	value, err := newJuicityAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := value.(*juicityAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.AddUsers([]core.User{{ID: id, UUID: userID}}); err != nil {
		t.Fatal(err)
	}
	client, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), &tls.Config{
		InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, NextProtos: []string{"h3"}, ServerName: "localhost",
	}, &quic.Config{HandshakeIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.CloseWithError(0, "test complete") })
	parsed := uuid.MustParse(userID)
	state := client.ConnectionState().TLS
	token, err := state.ExportKeyingMaterial(string(parsed[:]), []byte(userID), 32)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := client.OpenUniStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Write(append(append([]byte{juicityVersion, juicityAuthenticate}, parsed[:]...), token...)); err != nil {
		t.Fatal(err)
	}
	_ = auth.Close()
	return adapter, client
}

func openJuicityUDPStream(t *testing.T, client *quic.Conn) *quic.Stream {
	t.Helper()
	udp, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	initial := marshalJuicityAddress(juicityAddress{typ: 1, Host: "127.0.0.1", Port: 53})
	if _, err := udp.Write(append([]byte{juicityNetworkUDP}, initial...)); err != nil {
		t.Fatal(err)
	}
	return udp
}

// J1：域名目标按路由缓存解析结果，不再每包一次 DNS；IP 目标只算一次。
func TestJuicityUDPResolveCachedPerRoute(t *testing.T) {
	var resolves atomic.Int32
	old := juicityResolveUDP
	juicityResolveUDP = func(ctx context.Context, destination M.Socksaddr) (*net.UDPAddr, error) {
		resolves.Add(1)
		if destination.IsFqdn() {
			return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(destination.Port)}, nil
		}
		return resolveUDPAddr(ctx, destination)
	}
	t.Cleanup(func() { juicityResolveUDP = old })
	_, client := startJuicityLoopback(t, "0d6c5a8e-3a7b-4f1e-9c2d-5b8a7e6f4d31", 9201)
	udp := openJuicityUDPStream(t, client)
	defer udp.Close()
	const perTarget = 20
	for i := 0; i < perTarget; i++ {
		for _, target := range []juicityAddress{{typ: 3, Host: "Echo.Test", Port: 53}, {typ: 1, Host: "127.0.0.1", Port: 53}} {
			if _, err := udp.Write(marshalJuicityPacket(target, []byte(fmt.Sprintf("p%d", i)))); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 2*perTarget; i++ {
		if _, _, err := readJuicityPacket(udp); err != nil {
			t.Fatalf("第 %d 个回显：%v", i+1, err)
		}
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("两个目标各 %d 包，解析了 %d 次，期望每个路由 1 次", perTarget, got)
	}
}

// J4：每用户 UDP 路由数受 udpQuota 约束：超出的新目标丢包，已有路由照常；流结束后名额归还。
func TestJuicityUDPRouteQuota(t *testing.T) {
	adapter, client := startJuicityLoopback(t, "7e1f2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b", 9202)
	adapter.udpQuota.mu.Lock()
	adapter.udpQuota.limit = 2
	adapter.udpQuota.mu.Unlock()
	udp := openJuicityUDPStream(t, client)
	echo := func(port uint16) bool {
		t.Helper()
		if _, err := udp.Write(marshalJuicityPacket(juicityAddress{typ: 1, Host: "127.0.0.1", Port: port}, []byte("q"))); err != nil {
			t.Fatal(err)
		}
		_ = udp.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, _, err := readJuicityPacket(udp)
		return err == nil
	}
	if !echo(1001) || !echo(1002) {
		t.Fatal("上限以内的路由应能收发")
	}
	if echo(1003) {
		t.Fatal("超出上限的新目标仍被转发")
	}
	if !echo(1001) {
		t.Fatal("超限之后已有路由应照常")
	}
	adapter.udpQuota.mu.Lock()
	inUse := adapter.udpQuota.byUser[9202]
	adapter.udpQuota.mu.Unlock()
	if inUse != 2 {
		t.Fatalf("在途路由 %d，期望 2", inUse)
	}
	udp.CancelRead(0)
	_ = udp.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		adapter.udpQuota.mu.Lock()
		left := len(adapter.udpQuota.byUser)
		adapter.udpQuota.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("流结束后名额没归还：%v", adapter.udpQuota.byUser)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// J2：上行读帧、查路由、取缓存的解析结果，下行组帧，热路径都不分配。
func TestJuicityUDPHotPathAllocs(t *testing.T) {
	frame := marshalJuicityPacket(juicityAddress{typ: 3, Host: "Example.COM", Port: 443}, bytes.Repeat([]byte("x"), 1200))
	source := &repeatReader{frame: frame}
	reader := &juicityPacketReader{r: source}
	raw, _, err := reader.next()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[2:2+len("example.com")]) != "example.com" {
		t.Fatalf("域名没转小写：%q", raw)
	}
	route := &juicityUDPRoute{dest: M.ParseSocksaddrHostPort("example.com", 443), addr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 443}, resolved: time.Now()}
	routes := map[string]*juicityUDPRoute{string(raw): route}
	ctx := context.Background()
	up := testing.AllocsPerRun(1000, func() {
		raw, payload, err := reader.next()
		if err != nil || len(payload) != 1200 {
			t.Fatalf("next: %v", err)
		}
		r := routes[string(raw)]
		if r == nil {
			t.Fatal("路由没命中")
		}
		if _, err := r.resolve(ctx); err != nil {
			t.Fatal(err)
		}
	})
	framer := newJuicityDownlinkFramer(juicityAddress{typ: 1, Host: "192.0.2.1", Port: 443})
	from := netip.MustParseAddrPort("192.0.2.1:443")
	down := testing.AllocsPerRun(1000, func() {
		n := copy(framer.payload(), "response")
		out, _ := framer.frame(n, from, nil)
		if len(out) != 7+2+n {
			t.Fatalf("帧长 %d", len(out))
		}
	})
	if up != 0 || down != 0 {
		t.Fatalf("每包分配：上行 %.1f 次、下行 %.1f 次，期望 0", up, down)
	}
	// 帧可被原解析读回。
	n := copy(framer.payload(), "response")
	out, _ := framer.frame(n, from, nil)
	target, payload, err := readJuicityPacket(bytes.NewReader(out))
	if err != nil || target.Host != "192.0.2.1" || target.Port != 443 || string(payload) != "response" {
		t.Fatalf("回读 target=%+v payload=%q err=%v", target, payload, err)
	}
}

// repeatReader 无限重复同一帧。
type repeatReader struct {
	frame []byte
	off   int
}

func (r *repeatReader) Read(p []byte) (int, error) {
	n := copy(p, r.frame[r.off:])
	r.off = (r.off + n) % len(r.frame)
	return n, nil
}

// setJuicityUDPIdleTimeout 临时缩短路由空闲回收时间，测试结束还原。
func setJuicityUDPIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := juicityUDPIdleTimeout
	juicityUDPIdleTimeout = d
	t.Cleanup(func() { juicityUDPIdleTimeout = old })
}

func juicityQuotaInUse(adapter *juicityAdapter, userID int64) int {
	adapter.udpQuota.mu.Lock()
	defer adapter.udpQuota.mu.Unlock()
	return adapter.udpQuota.byUser[userID]
}

// 路由空闲回收（与 hy2 / TUIC 的 UDP 会话同为 5 分钟）：长期存活的 UDP 关联
// （BT DHT、P2P、STUN 这类一个 socket 发往大量目标）不能因为累计目标数到上限
// 就把新目标全丢掉。空闲的路由关掉上游 socket、退出读 goroutine、归还名额；
// 同一目标再发会新建；一直有流量的路由不回收。
func TestJuicityUDPRouteIdleReclaim(t *testing.T) {
	setJuicityUDPIdleTimeout(t, 300*time.Millisecond)
	var resolves atomic.Int32
	old := juicityResolveUDP
	juicityResolveUDP = func(ctx context.Context, destination M.Socksaddr) (*net.UDPAddr, error) {
		resolves.Add(1)
		return resolveUDPAddr(ctx, destination)
	}
	t.Cleanup(func() { juicityResolveUDP = old })
	const userID = 9203
	adapter, client := startJuicityLoopback(t, "3c2b1a09-8f7e-4d6c-9b5a-4e3f2d1c0b9a", userID)
	adapter.udpQuota.mu.Lock()
	adapter.udpQuota.limit = 2
	adapter.udpQuota.mu.Unlock()
	udp := openJuicityUDPStream(t, client)
	defer udp.Close()
	echo := func(port uint16) bool {
		t.Helper()
		if _, err := udp.Write(marshalJuicityPacket(juicityAddress{typ: 1, Host: "127.0.0.1", Port: port}, []byte("q"))); err != nil {
			t.Fatal(err)
		}
		_ = udp.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, _, err := readJuicityPacket(udp)
		return err == nil
	}
	baseG := runtime.NumGoroutine()
	if !echo(2001) || !echo(2002) {
		t.Fatal("上限以内的路由应能收发")
	}
	if echo(2003) {
		t.Fatal("超出上限的新目标仍被转发")
	}
	if got := juicityQuotaInUse(adapter, userID); got != 2 {
		t.Fatalf("在途路由 %d，期望 2", got)
	}
	// 2001 一直有流量（间隔短于空闲时间），2002 不再发：只回收 2002。
	for i := 0; i < 8; i++ {
		if !echo(2001) {
			t.Fatalf("活跃路由第 %d 次收发失败", i+1)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := juicityQuotaInUse(adapter, userID); got != 1 {
		t.Fatalf("空闲路由回收后在途 %d，期望 1（活跃的不回收）", got)
	}
	if got := resolves.Load(); got != 2 {
		t.Fatalf("活跃路由被重建过：解析 %d 次，期望 2", got)
	}
	// 名额回来了：新目标能建路由。
	if !echo(2003) {
		t.Fatal("回收后新目标仍被丢")
	}
	// 全部停发：两条都回收，名额归零，读 goroutine 退出。
	deadline := time.Now().Add(3 * time.Second)
	for juicityQuotaInUse(adapter, userID) != 0 || runtime.NumGoroutine() > baseG {
		if time.Now().After(deadline) {
			t.Fatalf("全部空闲后在途 %d、goroutine %d（基线 %d）", juicityQuotaInUse(adapter, userID), runtime.NumGoroutine(), baseG)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 同一目标再发：新建路由，照常收发。
	if !echo(2002) {
		t.Fatal("回收后同一目标再发应新建路由")
	}
	if got := juicityQuotaInUse(adapter, userID); got != 1 {
		t.Fatalf("重建后在途 %d，期望 1", got)
	}
}
