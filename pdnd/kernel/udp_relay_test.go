package kernel

// ============================================================================
//  UDP 中继的方向解耦回归
// ----------------------------------------------------------------------------
//  SOCKS5 UDP ASSOCIATE 与 Trojan UDP 曾在同一个循环里先带 250ms 读超时读上行、
//  再非阻塞地取一个下行包；客户端不发包时每个下行包都要等满 250ms，回程只剩
//  约 4 包/秒。VMess command=UDP 更糟：每个上行包只等一个回包（2 秒），没有上行
//  就没有下行。游戏、语音、QUIC 都是"发一个包、回一串包"，在这些路径上直接卡死。
//
//  这里的突发测试只让客户端发一个打开关联的包，之后只读：远端连发 1000 个带
//  序号的数据报，必须在几秒内全部回到客户端。清理测试守住解耦后的收尾语义：
//  控制连接关闭或适配器关闭时，每条经 DataPlane 打开的 PacketConn 都被关掉。
// ============================================================================

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/google/uuid"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	udpBurstCount       = 1000
	udpBurstPayloadSize = 64
	udpBurstWindow      = 5 * time.Second
)

// ----------------------------------------------------------------------------
//  假数据面：真实回环 UDP 套接字，记录每条 PacketConn 是否被关闭
// ----------------------------------------------------------------------------

type udpRelayTestPlane struct {
	testPlaneRecorder
	mu    sync.Mutex
	conns []*trackedPacketConn
}

type trackedPacketConn struct {
	net.PacketConn
	once   sync.Once
	closed chan struct{}
}

func (c *trackedPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.PacketConn.Close()
}

func (p *udpRelayTestPlane) DialTCP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.recordDial(destination)
	return nil, io.ErrUnexpectedEOF
}

func (p *udpRelayTestPlane) ListenUDP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.recordListen(destination)
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	_ = pc.SetReadBuffer(1 << 20)
	tracked := &trackedPacketConn{PacketConn: pc, closed: make(chan struct{})}
	p.mu.Lock()
	p.conns = append(p.conns, tracked)
	p.mu.Unlock()
	return tracked, nil
}

// waitAllClosed 断言至少打开过一条 PacketConn，且全部在 timeout 内被关闭。
func (p *udpRelayTestPlane) waitAllClosed(t *testing.T, timeout time.Duration) {
	t.Helper()
	p.mu.Lock()
	conns := append([]*trackedPacketConn(nil), p.conns...)
	p.mu.Unlock()
	if len(conns) == 0 {
		t.Fatal("data plane never opened a PacketConn")
	}
	deadline := time.After(timeout)
	for i, c := range conns {
		select {
		case <-c.closed:
		case <-deadline:
			t.Fatalf("routed PacketConn %d/%d still open after %v", i+1, len(conns), timeout)
		}
	}
}

// ----------------------------------------------------------------------------
//  远端：收到第一个数据报后向来源连发 count 个带序号的数据报
// ----------------------------------------------------------------------------

func startUDPBurstServer(t *testing.T, count int) *net.UDPAddr {
	t.Helper()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buf := make([]byte, 2048)
		_, src, readErr := server.ReadFromUDP(buf)
		if readErr != nil {
			return
		}
		payload := make([]byte, udpBurstPayloadSize)
		for i := 0; i < count; i++ {
			binary.BigEndian.PutUint32(payload, uint32(i))
			if _, writeErr := server.WriteToUDP(payload, src); writeErr != nil {
				return
			}
			// 每 8 个包歇 1ms：只为不把回环套接字缓冲区灌爆，修复后的中继远快于此。
			if i%8 == 7 {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	return server.LocalAddr().(*net.UDPAddr)
}

// expectUDPBurst 反复调用 read 收下行数据报，直到 count 个序号都到齐或 read 报错（读超时）。
func expectUDPBurst(t *testing.T, count int, read func() ([]byte, error)) {
	t.Helper()
	started := time.Now()
	seen := make(map[uint32]struct{}, count)
	var lastErr error
	for len(seen) < count {
		payload, err := read()
		if err != nil {
			lastErr = err
			break
		}
		if len(payload) != udpBurstPayloadSize {
			t.Fatalf("downlink payload length=%d want %d", len(payload), udpBurstPayloadSize)
		}
		seen[binary.BigEndian.Uint32(payload)] = struct{}{}
	}
	elapsed := time.Since(started)
	if len(seen) < count {
		t.Fatalf("received %d/%d downlink packets in %v without client uplink (last error: %v)", len(seen), count, elapsed.Round(time.Millisecond), lastErr)
	}
	t.Logf("received %d/%d downlink packets in %v", len(seen), count, elapsed.Round(time.Millisecond))
}

// ----------------------------------------------------------------------------
//  SOCKS5 UDP ASSOCIATE
// ----------------------------------------------------------------------------

func startSOCKSUDPRelayAdapter(t *testing.T, plane DataPlane) (*proxyAdapter, int) {
	t.Helper()
	port := reserveTCPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "socks", Listen: "127.0.0.1", Port: port}}
	value, err := newProxyAdapter("socks", spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := value.(*proxyAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.AddUsers([]core.User{{ID: 61, UUID: "socks-burst-user"}}); err != nil {
		t.Fatal(err)
	}
	return adapter, port
}

// openSOCKSUDPAssociation 完成用户名密码认证与 UDP ASSOCIATE，返回控制连接与关联端口。
func openSOCKSUDPAssociation(t *testing.T, port int, username string) (net.Conn, *net.UDPAddr) {
	t.Helper()
	control, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	_ = control.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(control)
	_, _ = control.Write([]byte{5, 1, 2})
	method := make([]byte, 2)
	if _, err := io.ReadFull(reader, method); err != nil || !bytes.Equal(method, []byte{5, 2}) {
		t.Fatalf("method=%v err=%v", method, err)
	}
	auth := append([]byte{1, byte(len(username))}, username...)
	auth = append(auth, byte(len(username)))
	auth = append(auth, username...)
	_, _ = control.Write(auth)
	status := make([]byte, 2)
	if _, err := io.ReadFull(reader, status); err != nil || !bytes.Equal(status, []byte{1, 0}) {
		t.Fatalf("auth=%v err=%v", status, err)
	}
	_, _ = control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
	reply := make([]byte, 10)
	if _, err := io.ReadFull(reader, reply); err != nil || reply[1] != 0 {
		t.Fatalf("associate reply=%v err=%v", reply, err)
	}
	_ = control.SetDeadline(time.Time{})
	return control, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(reply[8])<<8 | int(reply[9])}
}

func socksUDPDatagram(target *net.UDPAddr, payload []byte) []byte {
	ip := target.IP.To4()
	packet := []byte{0, 0, 0, 1, ip[0], ip[1], ip[2], ip[3], byte(target.Port >> 8), byte(target.Port)}
	return append(packet, payload...)
}

func TestSOCKS5UDPDownlinkBurstWithoutUplink(t *testing.T) {
	target := startUDPBurstServer(t, udpBurstCount)
	adapter, port := startSOCKSUDPRelayAdapter(t, &udpRelayTestPlane{})
	_, assoc := openSOCKSUDPAssociation(t, port, "socks-burst-user")
	client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetReadBuffer(1 << 20)
	if _, err := client.WriteToUDP(socksUDPDatagram(target, []byte("open")), assoc); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(udpBurstWindow))
	buf := make([]byte, 2048)
	expectUDPBurst(t, udpBurstCount, func() ([]byte, error) {
		n, _, err := client.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		// 只剥 IPv4 回包头（RSV RSV FRAG ATYP ADDR PORT），头部字节的合规性另有断言。
		if n < 10 || buf[3] != 1 {
			return nil, fmt.Errorf("socks5 UDP reply header=%x", buf[:min(n, 10)])
		}
		return append([]byte(nil), buf[10:n]...), nil
	})
	assertUDPRelayTraffic(t, adapter.SnapshotTraffic, 61, int64(len("open")), udpBurstCount*udpBurstPayloadSize)
}

// 回包头必须是 RFC 1928 的 RSV(0x0000) FRAG(0)：曾写成 05 00 00，校验 RSV 的客户端整包丢弃。
func TestSOCKS5UDPReplyHeaderIsRFC1928(t *testing.T) {
	packet, err := marshalSOCKSUDPDatagram(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 5353}, []byte("reply"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(packet[:4], []byte{0, 0, 0, 1}) {
		t.Fatalf("reply header=%x want 00000001", packet[:4])
	}
	destination, payload, err := parseSOCKSUDPDatagram(packet)
	if err != nil || destination.Host != "192.0.2.7" || destination.Port != 5353 || string(payload) != "reply" {
		t.Fatalf("round trip destination=%+v payload=%q err=%v", destination, payload, err)
	}
}

func TestSOCKS5UDPAssociateReleasesRoutes(t *testing.T) {
	for _, closeBy := range []string{"control", "adapter"} {
		t.Run(closeBy, func(t *testing.T) {
			target := startUDPBurstServer(t, 1)
			plane := &udpRelayTestPlane{}
			adapter, port := startSOCKSUDPRelayAdapter(t, plane)
			control, assoc := openSOCKSUDPAssociation(t, port, "socks-burst-user")
			client, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.WriteToUDP(socksUDPDatagram(target, []byte("open")), assoc); err != nil {
				t.Fatal(err)
			}
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := client.ReadFromUDP(make([]byte, 2048)); err != nil {
				t.Fatal(err)
			}
			if closeBy == "control" {
				_ = control.Close()
			} else {
				_ = adapter.Close()
			}
			plane.waitAllClosed(t, 2*time.Second)
		})
	}
}

// ----------------------------------------------------------------------------
//  Trojan UDP ASSOCIATE
// ----------------------------------------------------------------------------

func startTrojanUDPRelayAdapter(t *testing.T, plane DataPlane) (*trojanAdapter, int) {
	t.Helper()
	port := reserveTCPPort(t)
	adapter := &trojanAdapter{users: make(map[string]trojanUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.AddUsers([]core.User{{ID: 62, UUID: "trojan-burst-secret"}}); err != nil {
		t.Fatal(err)
	}
	return adapter, port
}

// openTrojanUDPSession 发 Trojan UDP 请求头和第一个数据报，之后客户端不再上行。
func openTrojanUDPSession(t *testing.T, port int, target *net.UDPAddr) net.Conn {
	t.Helper()
	client, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	header := []byte(trojanPasswordProof("trojan-burst-secret") + "\r\n")
	ip := target.IP.To4()
	header = append(header, trojanCommandUDP, 1, ip[0], ip[1], ip[2], ip[3], byte(target.Port>>8), byte(target.Port), '\r', '\n')
	destination := vlessDestination{IP: netip.AddrFrom4([4]byte(ip)), Host: target.IP.String(), Port: uint16(target.Port)}
	var first []byte
	if err := writeTrojanUDPPacket(&sliceWriter{dst: &first}, destination, []byte("open")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(append(header, first...)); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestTrojanUDPDownlinkBurstWithoutUplink(t *testing.T) {
	target := startUDPBurstServer(t, udpBurstCount)
	adapter, port := startTrojanUDPRelayAdapter(t, &udpRelayTestPlane{})
	client := openTrojanUDPSession(t, port, target)
	_ = client.SetReadDeadline(time.Now().Add(udpBurstWindow))
	scratch := make([]byte, trojanUDPMaxPacket)
	expectUDPBurst(t, udpBurstCount, func() ([]byte, error) {
		_, payload, err := readTrojanUDPPacket(client, scratch)
		return payload, err
	})
	assertUDPRelayTraffic(t, adapter.SnapshotTraffic, 62, int64(len("open")), udpBurstCount*udpBurstPayloadSize)
}

func TestTrojanUDPReleasesRoutes(t *testing.T) {
	for _, closeBy := range []string{"client", "adapter"} {
		t.Run(closeBy, func(t *testing.T) {
			target := startUDPBurstServer(t, 1)
			plane := &udpRelayTestPlane{}
			adapter, port := startTrojanUDPRelayAdapter(t, plane)
			client := openTrojanUDPSession(t, port, target)
			_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := readTrojanUDPPacket(client, make([]byte, trojanUDPMaxPacket)); err != nil {
				t.Fatal(err)
			}
			if closeBy == "client" {
				_ = client.Close()
			} else {
				_ = adapter.Close()
			}
			plane.waitAllClosed(t, 2*time.Second)
		})
	}
}

// ----------------------------------------------------------------------------
//  VMess command=UDP
// ----------------------------------------------------------------------------

func startVMessUDPRelaySession(t *testing.T, plane DataPlane, target *net.UDPAddr) (*vmessAdapter, net.PacketConn) {
	t.Helper()
	port := reserveTCPPort(t)
	id := uuid.New()
	adapter := &vmessAdapter{users: make(map[string]vmessUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"security": "aes-128-gcm"}}}
	if err := adapter.AddUsers([]core.User{{ID: 63, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	client, err := vmessref.NewClient(id.String(), "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := client.DialPacketConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(target.Port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packetConn.Close() })
	if _, err := packetConn.WriteTo([]byte("open"), target); err != nil {
		t.Fatal(err)
	}
	return adapter, packetConn
}

func TestVMessUDPDownlinkBurstWithoutUplink(t *testing.T) {
	target := startUDPBurstServer(t, udpBurstCount)
	adapter, packetConn := startVMessUDPRelaySession(t, &udpRelayTestPlane{}, target)
	_ = packetConn.SetReadDeadline(time.Now().Add(udpBurstWindow))
	buf := make([]byte, 2048)
	expectUDPBurst(t, udpBurstCount, func() ([]byte, error) {
		n, _, err := packetConn.ReadFrom(buf)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), buf[:n]...), nil
	})
	assertUDPRelayTraffic(t, adapter.SnapshotTraffic, 63, int64(len("open")), udpBurstCount*udpBurstPayloadSize)
}

func TestVMessUDPReleasesRoutes(t *testing.T) {
	for _, closeBy := range []string{"client", "adapter"} {
		t.Run(closeBy, func(t *testing.T) {
			target := startUDPBurstServer(t, 1)
			plane := &udpRelayTestPlane{}
			adapter, packetConn := startVMessUDPRelaySession(t, plane, target)
			_ = packetConn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := packetConn.ReadFrom(make([]byte, 2048)); err != nil {
				t.Fatal(err)
			}
			if closeBy == "client" {
				_ = packetConn.Close()
			} else {
				_ = adapter.Close()
			}
			plane.waitAllClosed(t, 2*time.Second)
		})
	}
}

// ----------------------------------------------------------------------------
//  计量：上行只计打开关联的那个包，下行计全部突发包
// ----------------------------------------------------------------------------

// assertUDPRelayTraffic 累加 SnapshotTraffic（每次快照会清零）直到与期望相等。
// 下行计量发生在写给客户端之后，所以客户端收齐时最后几个包可能还没计上，要轮询。
func assertUDPRelayTraffic(t *testing.T, snapshot func() ([]core.UserTraffic, error), userID, wantUp, wantDown int64) {
	t.Helper()
	var up, down int64
	deadline := time.Now().Add(2 * time.Second)
	for {
		traffic, err := snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range traffic {
			if entry.ID != userID {
				t.Fatalf("traffic metered to user %d, want %d", entry.ID, userID)
			}
			up += entry.Upload
			down += entry.Download
		}
		if up == wantUp && down == wantDown {
			return
		}
		if up > wantUp || down > wantDown || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("user %d traffic upload=%d download=%d want upload=%d download=%d", userID, up, down, wantUp, wantDown)
}
