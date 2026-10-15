package kernel

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	SS "github.com/sagernet/sing-shadowsocks2"
	M "github.com/sagernet/sing/common/metadata"
)

func TestNativeShadowsocksRejectsUnsupportedModes(t *testing.T) {
	base := core.InboundConfig{Protocol: "shadowsocks", Port: 443, Raw: map[string]any{"method": "aes-128-gcm"}}
	for _, tc := range []struct {
		name       string
		mutate     func(map[string]any)
		wantSubstr string
	}{
		{name: "2022", mutate: func(raw map[string]any) { raw["method"] = "2022-blake3-aes-256-gcm" }},
		{name: "plugin", mutate: func(raw map[string]any) { raw["plugin"] = "obfs-local" }},
		{name: "quic", mutate: func(raw map[string]any) { raw["network"] = "quic" }, wantSubstr: "不再接受 network 字段"},
		{name: "network-udp", mutate: func(raw map[string]any) { raw["network"] = "udp" }, wantSubstr: "不再接受 network 字段"},
		{name: "network-tcp", mutate: func(raw map[string]any) { raw["network"] = "tcp" }, wantSubstr: "不再接受 network 字段"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := make(map[string]any, len(base.Raw))
			for key, value := range base.Raw {
				raw[key] = value
			}
			tc.mutate(raw)
			err := (&shadowsocksAdapter{}).Validate(InboundSpec{Config: core.InboundConfig{Protocol: base.Protocol, Port: base.Port, Raw: raw}})
			if err == nil {
				t.Fatal("unsupported shadowsocks mode accepted")
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("err=%q，期望包含 %q", err.Error(), tc.wantSubstr)
			}
		})
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
	port := reserveTCPAndUDPPort(t)
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port, Raw: map[string]any{"method": "aes-128-gcm"}}}
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
