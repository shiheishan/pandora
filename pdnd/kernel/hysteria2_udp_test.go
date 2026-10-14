package kernel

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	M "github.com/sagernet/sing/common/metadata"
)

// hy2EchoUDP 起一个真实的 UDP 回显服务。
func hy2EchoUDP(t *testing.T) net.Addr {
	t.Helper()
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		buffer := make([]byte, 64<<10)
		for {
			n, addr, err := echo.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buffer[:n], addr)
		}
	}()
	return echo.LocalAddr()
}

type hy2RawPlane struct{ raw bool }

func (hy2RawPlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, fmt.Errorf("no tcp")
}

func (p hy2RawPlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if p.raw {
		return hy2LoadRawPacketConn{conn}, nil
	}
	return hy2LoadPacketConn{conn}, nil
}

// 经真实 Hysteria2 客户端走一遍 UDP：小包、要分片的 1200 字节包、超 MTU 的大包，
// 上游分别是能交出裸 socket（批量收发）与不能交出（逐包）两种，回显逐字节一致，
// 流量计数与负载字节数严格相等。
func TestHysteria2UDPRelayEndToEnd(t *testing.T) {
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	for _, tc := range []struct{ raw, batch bool }{{true, true}, {true, false}, {false, false}} {
		raw := tc.raw
		// 非 Linux 上强制走批量路径（每次只收发一包），批量逻辑在本机也有覆盖。
		hy2UDPBatchSupported = tc.batch
		t.Run(fmt.Sprintf("raw=%v/batch=%v", tc.raw, tc.batch), func(t *testing.T) {
			certPath, keyPath := testXHTTPServerCertFiles(t)
			probe, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.LocalAddr().(*net.UDPAddr).Port
			_ = probe.Close()
			spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
				"cert_path": certPath, "key_path": keyPath, "network": "udp", "udp_queue_size": 64,
			}}}
			value, err := newHysteria2Adapter(spec)
			if err != nil {
				t.Fatal(err)
			}
			adapter := value.(*hysteria2Adapter)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: hy2RawPlane{raw: raw}}); err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			if err := adapter.AddUsers([]core.User{{ID: 9, UUID: "relay-secret"}}); err != nil {
				t.Fatal(err)
			}
			client, err := hy2.NewClient(hy2.ClientOptions{
				Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
				ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)), Password: "relay-secret",
				TLSConfig: &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseWithError(nil)
			udp, err := client.ListenPacket(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer udp.Close()
			echo := M.SocksaddrFromNet(hy2EchoUDP(t))
			var total int64
			reply := make([]byte, 64<<10)
			for i, size := range []int{1, 64, 1200, 1200, 3000, 512, 1200} {
				payload := bytes.Repeat([]byte{byte(i + 1)}, size)
				if _, err := udp.WriteTo(payload, echo); err != nil {
					t.Fatal(err)
				}
				_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
				n, from, err := udp.ReadFrom(reply)
				if err != nil || !bytes.Equal(reply[:n], payload) {
					t.Fatalf("第 %d 个包（%d 字节）回显 n=%d err=%v", i, size, n, err)
				}
				if M.SocksaddrFromNet(from).Unwrap() != echo {
					t.Fatalf("回包来源 %v，期望 %v", from, echo)
				}
				total += int64(size)
			}
			traffic, err := adapter.SnapshotTraffic()
			if err != nil || len(traffic) != 1 || traffic[0].ID != 9 || traffic[0].Upload != total || traffic[0].Download != total {
				t.Fatalf("流量 %+v，期望上下行各 %d", traffic, total)
			}
		})
	}
}

// 批量写：同批多目标都送达；IPv4 裸 socket 走 WriteBatch，其余逐包。
func TestHy2BatchWriterDeliversAll(t *testing.T) {
	supported := hy2UDPBatchSupported
	defer func() { hy2UDPBatchSupported = supported }()
	hy2UDPBatchSupported = true
	sinks := make([]net.PacketConn, 2)
	for i := range sinks {
		sink, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		sinks[i] = sink
	}
	for _, raw := range []bool{true, false} {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		var upstream net.PacketConn = hy2LoadPacketConn{conn}
		if raw {
			upstream = hy2LoadRawPacketConn{conn}
		}
		writer := newHy2BatchWriter(upstream, newHy2UDPUpstream(upstream).batch)
		if (writer.batch != nil) != raw {
			t.Fatalf("raw=%v 时批量通道 %v", raw, writer.batch != nil)
		}
		resolver := &hy2UDPResolver{ctx: context.Background()}
		writer.reset()
		var want int64
		for i := 0; i < 10; i++ {
			payload := []byte(fmt.Sprintf("packet-%02d", i))
			want += int64(len(payload))
			writer.add(resolver, payload, M.SocksaddrFromNet(sinks[i%2].LocalAddr()), M.Socksaddr{})
		}
		written, err := writer.flush()
		if err != nil || written != want {
			t.Fatalf("raw=%v 写出 %d/%d err=%v", raw, written, want, err)
		}
		buffer := make([]byte, 64)
		for i := 0; i < 10; i++ {
			sink := sinks[i%2]
			_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, _, err := sink.ReadFrom(buffer)
			if err != nil || string(buffer[:n]) != fmt.Sprintf("packet-%02d", i) {
				t.Fatalf("raw=%v 第 %d 包 got %q err=%v", raw, i, buffer[:n], err)
			}
		}
		_ = conn.Close()
	}
}

// 上游解析失败的包丢弃，不影响同批其它包；目标缺省时回落到会话初始目标。
func TestHy2BatchWriterSkipsUnresolvable(t *testing.T) {
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writer := newHy2BatchWriter(hy2LoadRawPacketConn{conn}, nil)
	resolver := &hy2UDPResolver{ctx: context.Background()}
	writer.reset()
	writer.add(resolver, []byte("bad"), M.Socksaddr{Fqdn: " ", Port: 53}, M.Socksaddr{})
	writer.add(resolver, []byte("fallback"), M.Socksaddr{}, M.SocksaddrFromNet(sink.LocalAddr()))
	if written, err := writer.flush(); err != nil || written != int64(len("fallback")) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	buffer := make([]byte, 64)
	_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, _, err := sink.ReadFrom(buffer); err != nil || string(buffer[:n]) != "fallback" {
		t.Fatalf("got %q err=%v", buffer[:n], err)
	}
}

func TestHysteria2UDPQueueSizeValidation(t *testing.T) {
	for _, tc := range []struct {
		value any
		ok    bool
		want  int
	}{{nil, true, 512}, {64, true, 64}, {"2048", true, 2048}, {8, false, 0}, {1 << 20, false, 0}, {-1, false, 0}, {"x", false, 0}} {
		raw := map[string]any{}
		if tc.value != nil {
			raw["udp_queue_size"] = tc.value
		}
		got, err := hysteria2UDPQueueSize(raw)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Fatalf("udp_queue_size=%v → %d err=%v", tc.value, got, err)
		}
	}
}
