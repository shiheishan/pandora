package kernel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	"github.com/google/uuid"
	vmessref "github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

func TestVMessKDFMatchesReference(t *testing.T) {
	key := []byte("0123456789abcdef")
	got := vmessKDF(key, "VMess Header AEAD Key", []byte("auth-id"), []byte("nonce"))
	want := vmessref.KDF(key, "VMess Header AEAD Key", []byte("auth-id"), []byte("nonce"))
	if string(got) != string(want) {
		t.Fatalf("KDF mismatch: got %x want %x", got, want)
	}
}

func TestVMessReplayCache(t *testing.T) {
	a := &vmessAdapter{}
	var id [16]byte
	if !a.acceptAuthID(id) {
		t.Fatal("first auth id was rejected")
	}
	if a.acceptAuthID(id) {
		t.Fatal("replayed auth id was accepted")
	}
}

func TestVMessNativeNoneLoopback(t *testing.T) {
	runVMessNativeLoopback(t, "none")
}

func TestVMessNativeAES128Loopback(t *testing.T) {
	runVMessNativeLoopback(t, "aes-128-gcm")
}

func TestVMessNativeChaChaLoopback(t *testing.T) {
	runVMessNativeLoopback(t, "chacha20-poly1305")
}

type vmessUDPTestPlane struct {
	testPlaneRecorder
}

func (p *vmessUDPTestPlane) DialTCP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.recordDial(destination)
	return nil, io.ErrUnexpectedEOF
}

func (p *vmessUDPTestPlane) ListenUDP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.recordListen(destination)
	return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
}

func TestVMessNativeUDPNoneLoopback(t *testing.T) {
	runVMessNativeUDPLoopback(t, "none")
}

func TestVMessNativeUDPAES128Loopback(t *testing.T) {
	runVMessNativeUDPLoopback(t, "aes-128-gcm")
}

func TestVMessNativeUDPChaChaLoopback(t *testing.T) {
	runVMessNativeUDPLoopback(t, "chacha20-poly1305")
}

func TestVMessNativeMuxUDPLoopback(t *testing.T) {
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buf := make([]byte, 2048)
		for i := 0; i < 2; i++ {
			n, addr, readErr := upstream.ReadFromUDP(buf)
			if readErr != nil {
				return
			}
			_, _ = upstream.WriteToUDP(buf[:n], addr)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"security": "aes-128-gcm"}}}
	if err := a.AddUsers([]core.User{{ID: 93, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: &vmessUDPTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := vmessref.NewClient(id.String(), "aes-128-gcm", 0)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := client.DialXUDPPacketConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.LocalAddr().(*net.UDPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	_ = packetConn.SetDeadline(time.Now().Add(4 * time.Second))
	destination := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: upstream.LocalAddr().(*net.UDPAddr).Port}
	for _, payload := range [][]byte{[]byte("native-vmess-mux"), []byte("native-vmess-mux-2")} {
		if _, err := packetConn.WriteTo(payload, destination); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 2048)
		n, _, err := packetConn.ReadFrom(response)
		if err != nil {
			t.Fatal(err)
		}
		if string(response[:n]) != string(payload) {
			t.Fatalf("response=%q", response[:n])
		}
	}
}

func runVMessNativeUDPLoopback(t *testing.T, security string) {
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buf := make([]byte, 2048)
		n, addr, e := upstream.ReadFromUDP(buf)
		if e == nil {
			_, _ = upstream.WriteToUDP(buf[:n], addr)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"security": security}}}
	if err := a.AddUsers([]core.User{{ID: 92, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: &vmessUDPTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := vmessref.NewClient(id.String(), security, 0)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := client.DialPacketConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.LocalAddr().(*net.UDPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	_ = packetConn.SetDeadline(time.Now().Add(4 * time.Second))
	destination := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: upstream.LocalAddr().(*net.UDPAddr).Port}
	if _, err := packetConn.WriteTo([]byte("native-vmess-udp"), destination); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2048)
	n, _, err := packetConn.ReadFrom(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(response[:n]) != "native-vmess-udp" {
		t.Fatalf("response=%q", response[:n])
	}
	var traffic []core.UserTraffic
	for i := 0; i < 100; i++ {
		traffic, err = a.SnapshotTraffic()
		if err != nil {
			t.Fatal(err)
		}
		if len(traffic) > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(traffic) != 1 || traffic[0].ID != 92 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func runVMessNativeLoopback(t *testing.T, security string) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upDone := make(chan struct{})
	go func() {
		defer close(upDone)
		conn, e := upstream.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	a := &vmessAdapter{users: make(map[string]vmessUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"security": security}}}
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := a.AddUsers([]core.User{{ID: 91, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := vmessref.NewClient(id.String(), security, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := client.DialConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := conn.Write([]byte("native-vmess")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("native-vmess"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "native-vmess" {
		t.Fatalf("echo=%q", got)
	}
	// 客户端请求头里写的是 upstream 的真实地址，解析结果必须和它一致。
	assertDialedTarget(t, plane, upstream.Addr().String())
	_ = a.Close()
	select {
	case <-upDone:
	case <-time.After(time.Second):
	}
	traffic, err := a.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 91 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func itoa(v int) string {
	const digits = "0123456789"
	if v == 0 {
		return "0"
	}
	b := make([]byte, 0, 6)
	for v > 0 {
		b = append([]byte{digits[v%10]}, b...)
		v /= 10
	}
	return string(b)
}
