package kernel

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	ss2022ref "github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	SS "github.com/sagernet/sing-shadowsocks2"
	M "github.com/sagernet/sing/common/metadata"
)

func TestNativeShadowsocksRejectsUnsupportedModes(t *testing.T) {
	base := core.InboundConfig{Protocol: "shadowsocks", Port: 443, Raw: map[string]any{"method": "aes-128-gcm"}}
	for name, mutate := range map[string]func(map[string]any){
		"2022":   func(raw map[string]any) { raw["method"] = "2022-blake3-aes-256-gcm" },
		"plugin": func(raw map[string]any) { raw["plugin"] = "obfs-local" },
		"quic":   func(raw map[string]any) { raw["network"] = "quic" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := make(map[string]any, len(base.Raw))
			for key, value := range base.Raw {
				raw[key] = value
			}
			mutate(raw)
			if err := (&shadowsocksAdapter{}).Validate(InboundSpec{Config: core.InboundConfig{Protocol: base.Protocol, Port: base.Port, Raw: raw}}); err == nil {
				t.Fatal("unsupported shadowsocks mode accepted")
			}
		})
	}
}

func TestNativeShadowsocks2022LoopbackWithReferenceClient(t *testing.T) {
	for _, tc := range []struct {
		method string
		keyLen int
	}{
		{method: "2022-blake3-aes-128-gcm", keyLen: 16},
		{method: "2022-blake3-aes-256-gcm", keyLen: 32},
		{method: "2022-blake3-chacha20-poly1305", keyLen: 32},
	} {
		t.Run(tc.method, func(t *testing.T) { runNativeShadowsocks2022Reference(t, tc.method, tc.keyLen) })
	}
}

func runNativeShadowsocks2022Reference(t *testing.T, methodName string, keyLen int) {
	psk := make([]byte, keyLen)
	for i := range psk {
		psk[i] = byte(i + 1)
	}
	runNativeShadowsocks2022ReferencePSKs(t, methodName, [][]byte{psk})
}

func runNativeShadowsocks2022ReferencePSKs(t *testing.T, methodName string, psks [][]byte) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
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

	passwordParts := make([]string, 0, len(psks))
	for _, psk := range psks {
		passwordParts = append(passwordParts, base64.StdEncoding.EncodeToString(psk))
	}
	password := strings.Join(passwordParts, ":")
	spec := InboundSpec{Config: core.InboundConfig{
		Protocol: "shadowsocks",
		Listen:   "127.0.0.1",
		Port:     port,
		Raw: map[string]any{
			"method":   methodName,
			"password": password,
		},
	}}
	adapterValue, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter, ok := adapterValue.(*ss2022Adapter)
	if !ok {
		t.Fatalf("adapter type=%T", adapterValue)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 222, UUID: "ss2022-user"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	method, err := ss2022ref.New(methodName, psks, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := method.DialConn(raw, M.ParseSocksaddrHostPort("127.0.0.1", uint16(upstream.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("native-shadowsocks-2022")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(payload))
	if _, err := io.ReadFull(client, response); err != nil {
		adapter.mu.RLock()
		serverErr := adapter.lastErr
		adapter.mu.RUnlock()
		t.Fatalf("read response: %v (server: %v)", err, serverErr)
	}
	if string(response) != string(payload) {
		t.Fatalf("response=%q", response)
	}
	// 客户端请求头里写的是 upstream 的真实地址，解析结果必须和它一致。
	assertDialedTarget(t, plane, upstream.Addr().String())
	_ = client.Close()
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 222 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestNativeShadowsocks2022MultiPSKLoopback(t *testing.T) {
	first := make([]byte, 16)
	second := make([]byte, 16)
	for i := range first {
		first[i] = byte(i + 1)
		second[i] = byte(0xa0 + i)
	}
	runNativeShadowsocks2022ReferencePSKs(t, "2022-blake3-aes-128-gcm", [][]byte{first, second})
}

func TestNativeShadowsocks2022UDPReferenceLoopback(t *testing.T) {
	for _, tc := range []struct {
		method string
		keyLen int
	}{
		{method: "2022-blake3-aes-128-gcm", keyLen: 16},
		{method: "2022-blake3-aes-256-gcm", keyLen: 32},
		{method: "2022-blake3-chacha20-poly1305", keyLen: 32},
	} {
		t.Run(tc.method, func(t *testing.T) { runNativeShadowsocks2022UDPReference(t, tc.method, tc.keyLen) })
	}
}

func TestNativeShadowsocks2022UDPMultiPSKReferenceLoopback(t *testing.T) {
	first := make([]byte, 32)
	second := make([]byte, 32)
	for i := range first {
		first[i] = byte(i + 1)
		second[i] = byte(0xa0 + i)
	}
	runNativeShadowsocks2022UDPReferencePSKsWithClient(t, "2022-blake3-chacha20-poly1305", 32, [][]byte{first, second}, [][]byte{second})
}

func TestNativeShadowsocks2022UDPSessionGC(t *testing.T) {
	now := time.Now()
	adapter := &ss2022Adapter{udp: map[string]*ss2022UDPSession{
		"stale":   {lastSeen: now.Add(-ss2022UDPIdleTimeout - time.Second)},
		"fresh":   {lastSeen: now.Add(-time.Second)},
		"unknown": {},
	}}
	adapter.cleanupUDPSessions(now)
	if _, ok := adapter.udp["stale"]; ok {
		t.Fatal("stale UDP session was not removed")
	}
	if _, ok := adapter.udp["fresh"]; !ok {
		t.Fatal("fresh UDP session was removed")
	}
	if _, ok := adapter.udp["unknown"]; !ok {
		t.Fatal("session without timestamp was removed")
	}
}

func runNativeShadowsocks2022UDPReference(t *testing.T, methodName string, keyLen int) {
	psk := make([]byte, keyLen)
	for i := range psk {
		psk[i] = byte(i + 7)
	}
	runNativeShadowsocks2022UDPReferencePSKs(t, methodName, keyLen, [][]byte{psk})
}

func runNativeShadowsocks2022UDPReferencePSKs(t *testing.T, methodName string, keyLen int, psks [][]byte) {
	runNativeShadowsocks2022UDPReferencePSKsWithClient(t, methodName, keyLen, psks, psks)
}

func runNativeShadowsocks2022UDPReferencePSKsWithClient(t *testing.T, methodName string, keyLen int, psks, clientPSKs [][]byte) {
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		buffer := make([]byte, 2048)
		for i := 0; i < 2; i++ {
			n, addr, readErr := upstream.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			_, _ = upstream.WriteToUDP(buffer[:n], addr)
		}
	}()
	reserved, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.LocalAddr().(*net.UDPAddr).Port
	_ = reserved.Close()
	encodedPSKs := make([]string, 0, len(psks))
	for _, psk := range psks {
		if len(psk) != keyLen {
			t.Fatalf("PSK length=%d, want %d", len(psk), keyLen)
		}
		encodedPSKs = append(encodedPSKs, base64.StdEncoding.EncodeToString(psk))
	}
	password := strings.Join(encodedPSKs, ":")
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"method": methodName, "password": password, "network": "udp"}}}
	adapterValue, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*ss2022Adapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &ssUDPTestPlane{target: upstream.LocalAddr().(*net.UDPAddr)}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 223, UUID: "ss2022-udp-user"}}); err != nil {
		t.Fatal(err)
	}
	clientRaw, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer clientRaw.Close()
	method, err := ss2022ref.New(methodName, clientPSKs, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := method.DialPacketConn(clientRaw)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	target := upstream.LocalAddr().(*net.UDPAddr)
	for _, payload := range [][]byte{[]byte("native-shadowsocks-2022-udp"), []byte("native-shadowsocks-2022-udp-2")} {
		if _, err := client.WriteTo(payload, target); err != nil {
			t.Fatal(err)
		}
		response := make([]byte, 2048)
		n, _, err := client.ReadFrom(response)
		if err != nil {
			t.Fatal(err)
		}
		if string(response[:n]) != string(payload) {
			t.Fatalf("response=%q", response[:n])
		}
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 223 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestShadowsocksAdapterLoopbackTCPAndTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
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
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"method": "aes-128-gcm"}}}
	adapterValue, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*shadowsocksAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	const password = "native-ss-password"
	if err := adapter.AddUsers([]core.User{{ID: 88, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	stream := method.DialEarlyConn(client, M.ParseSocksaddrHostPort("127.0.0.1", 443))
	if _, err := stream.Write([]byte("native-shadowsocks")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("native-shadowsocks"))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "native-shadowsocks" {
		t.Fatalf("echo=%q", got)
	}
	// 客户端请求头里写的是 127.0.0.1:443。
	assertDialedTarget(t, plane, "127.0.0.1:443")
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 88 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestShadowsocksAdapterLoopbackUDPAndTraffic(t *testing.T) {
	upstream, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		buffer := make([]byte, 2048)
		n, addr, readErr := upstream.ReadFromUDP(buffer)
		if readErr == nil {
			_, _ = upstream.WriteToUDP(buffer[:n], addr)
		}
	}()
	reserved, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.LocalAddr().(*net.UDPAddr).Port
	_ = reserved.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"method": "aes-128-gcm", "network": "udp"}}}
	adapterValue, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*shadowsocksAdapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &ssUDPTestPlane{target: upstream.LocalAddr().(*net.UDPAddr)}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	const password = "native-ss-udp-password"
	if err := adapter.AddUsers([]core.User{{ID: 89, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	serverAddr := adapter.packet.LocalAddr().(*net.UDPAddr)
	clientRaw, err := net.DialUDP("udp", nil, serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer clientRaw.Close()
	method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	client := method.DialPacketConn(clientRaw)
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: upstream.LocalAddr().(*net.UDPAddr).Port}
	if _, err := client.WriteTo([]byte("native-shadowsocks-udp"), target); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 2048)
	n, _, err := client.ReadFrom(response)
	if err != nil {
		t.Fatal(err)
	}
	if string(response[:n]) != "native-shadowsocks-udp" {
		t.Fatalf("response=%q", response[:n])
	}
	// SOCKS5 UDP 包头里写的目标必须原样传到数据面。
	assertListenedTarget(t, plane, target.String())
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 89 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

type ssUDPTestPlane struct {
	testPlaneRecorder
	target *net.UDPAddr
}

func (p *ssUDPTestPlane) DialTCP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.Conn, error) {
	p.recordDial(destination)
	return nil, io.ErrUnexpectedEOF
}

func (p *ssUDPTestPlane) ListenUDP(_ context.Context, _ route.Meta, destination M.Socksaddr) (net.PacketConn, error) {
	p.recordListen(destination)
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
}
