package kernel

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/gofrs/uuid/v5"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	tuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
)

// quicUDPKickCase 是一种 QUIC 类协议：起适配器、按用户开一个 UDP 隧道。
type quicUDPKickCase struct {
	name     string
	protocol string
	newA     func(InboundSpec) (Adapter, error)
	users    []core.User
	// listen 用 users[i] 的凭据连上 127.0.0.1:port，开一个 UDP 隧道。
	listen func(t *testing.T, ctx context.Context, port int, user core.User) net.PacketConn
}

func quicUDPKickCases() []quicUDPKickCase {
	tlsConfig := func(alpn ...string) *hysteria2TLSConfig {
		return &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: alpn}} //nolint:gosec -- 测试证书。
	}
	return []quicUDPKickCase{
		{
			name: "hysteria2", protocol: "hysteria2", newA: newHysteria2Adapter,
			users: []core.User{{ID: 7101, UUID: "kick-victim-secret"}, {ID: 7102, UUID: "kick-bystander-secret"}},
			listen: func(t *testing.T, ctx context.Context, port int, user core.User) net.PacketConn {
				client, err := hy2.NewClient(hy2.ClientOptions{
					Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
					ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)), Password: user.UUID,
					TLSConfig: tlsConfig(),
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.CloseWithError(nil) })
				udp, err := client.ListenPacket(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = udp.Close() })
				return udp
			},
		},
		{
			name: "tuic", protocol: "tuic", newA: newTUICAdapter,
			users: []core.User{{ID: 7201, UUID: "0c3a7c1e-3f1b-4f7a-9a51-6a0f2b7c1d01"}, {ID: 7202, UUID: "0c3a7c1e-3f1b-4f7a-9a51-6a0f2b7c1d02"}},
			listen: func(t *testing.T, ctx context.Context, port int, user core.User) net.PacketConn {
				parsed := uuid.Must(uuid.FromString(user.UUID))
				client, err := tuic.NewClient(tuic.ClientOptions{
					Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
					ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)), UUID: [16]byte(parsed), Password: user.UUID,
					TLSConfig: tlsConfig("h3"),
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.CloseWithError(nil) })
				udp, err := client.ListenPacket(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = udp.Close() })
				return udp
			},
		},
	}
}

// userCounterRefs 是一个用户计数器上还挂着几个会话（转发 goroutine 退出时才减）。
func userCounterRefs(s *userSessions, id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.traffic[id]; c != nil {
		return c.refs
	}
	return 0
}

// 删用户即断线也覆盖 QUIC 类协议的 UDP 会话：原先 hy2 / TUIC 的 UDP 会话不进用户
// 连接表，删用户后要等空闲超时（默认 5 分钟）才断。现在 1 秒内转发结束、上游
// socket 关掉，其他用户的 UDP 会话不受影响，流量照常按用户计入。
func TestDelUsersEndsQUICUDPSessions(t *testing.T) {
	for _, tc := range quicUDPKickCases() {
		t.Run(tc.name, func(t *testing.T) {
			certPath, keyPath := testXHTTPServerCertFiles(t)
			probe, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.LocalAddr().(*net.UDPAddr).Port
			_ = probe.Close()
			spec := InboundSpec{Config: core.InboundConfig{Protocol: tc.protocol, Listen: "127.0.0.1", Port: port, Raw: map[string]any{
				"cert_path": certPath, "key_path": keyPath, "network": "udp",
			}}}
			adapter, err := tc.newA(spec)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: hy2RawPlane{raw: true}}); err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			if err := adapter.AddUsers(tc.users); err != nil {
				t.Fatal(err)
			}
			table := adapter.(sessionTracker).userSessionTable()
			echo := M.SocksaddrFromNet(hy2EchoUDP(t))
			victim, bystander := tc.users[0], tc.users[1]
			victimUDP := tc.listen(t, ctx, port, victim)
			keepUDP := tc.listen(t, ctx, port, bystander)
			for _, udp := range []net.PacketConn{victimUDP, keepUDP} {
				if err := udpEchoOnce(udp, echo, "hello"); err != nil {
					t.Fatal(err)
				}
			}
			if live := table.liveCount(); live != 2 {
				t.Fatalf("两个用户各一个 UDP 会话，登记了 %d 个", live)
			}
			if refs := userCounterRefs(table, victim.ID); refs != 1 {
				t.Fatalf("被删用户的计数器挂着 %d 个会话，期望 1", refs)
			}

			if err := adapter.DelUsers([]string{victim.UUID}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for userCounterRefs(table, victim.ID) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("被删用户的 UDP 会话 1 秒内没有结束")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if live := table.liveCount(); live != 1 {
				t.Fatalf("删人后在途会话=%d，期望只剩 1 个", live)
			}
			// 被删用户再发包：服务端按口令查不到人，开不出新会话，收不到回显。
			_, _ = victimUDP.WriteTo([]byte("again"), echo)
			_ = victimUDP.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
			if n, _, err := victimUDP.ReadFrom(make([]byte, 16)); err == nil {
				t.Fatalf("被删用户的 UDP 隧道仍然有回显（%d 字节）", n)
			}
			if err := udpEchoOnce(keepUDP, echo, "still-here"); err != nil {
				t.Fatalf("其他用户受到影响：%v", err)
			}
			traffic, err := adapter.SnapshotTraffic()
			if err != nil {
				t.Fatal(err)
			}
			byID := map[int64]core.UserTraffic{}
			for _, item := range traffic {
				byID[item.ID] = item
			}
			if byID[victim.ID].Upload != 5 || byID[victim.ID].Download != 5 {
				t.Fatalf("被删用户的流量 %+v，期望上下行各 5 字节", byID[victim.ID])
			}
			if byID[bystander.ID].Upload != 15 || byID[bystander.ID].Download != 15 {
				t.Fatalf("其他用户的流量 %+v，期望上下行各 15 字节", byID[bystander.ID])
			}
		})
	}
}

func udpEchoOnce(udp net.PacketConn, target M.Socksaddr, payload string) error {
	if _, err := udp.WriteTo([]byte(payload), target); err != nil {
		return err
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	reply := make([]byte, 64)
	n, _, err := udp.ReadFrom(reply)
	if err != nil {
		return err
	}
	if string(reply[:n]) != payload {
		return &net.AddrError{Err: "回显不一致：" + string(reply[:n]), Addr: target.String()}
	}
	return nil
}
