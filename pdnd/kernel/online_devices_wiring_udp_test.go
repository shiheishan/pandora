package kernel

// 在线设备接线的 UDP 路径：UDP 会话同样经 onlineDevices 登记、收尾时撤销。
// 每个协议：同 IP 上一个 UDP 会话加另一条连接（能共用入站的配一条 TCP，SS / SS2022
// 的 UDP 入站只收 UDP，配第二个 UDP 会话），设备上限 1 时都放行；关掉 UDP 会话后
// 仍在线；全部关掉才离线。漏掉 UDP 路径的 leave 会让最后一步永远等不到离线。
//
// SS / SS2022 的 UDP 按包处理（收一包、转发、等回包、回给客户端），「会话」就是一包
// 在途的时长：回显上游先扣住包，关会话时才回。用户与口令全是虚构的。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gofrsuuid "github.com/gofrs/uuid/v5"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	tuic "github.com/sagernet/sing-quic/tuic"
	ss2022ref "github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	SS "github.com/sagernet/sing-shadowsocks2"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
)

type deviceUDPCase struct {
	name, proto, uuid string
	raw               func(t *testing.T) map[string]any
	// hold 为真时回显上游扣住每个包，直到会话被关（按包处理的 SS / SS2022）。
	hold bool
	// openUDP 建一个经代理到回显上游的 UDP 会话并确认打通（hold 时确认上游收到）。
	openUDP func(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error)
	// openTCP 为 nil 时第二条连接也是 UDP 会话。
	openTCP func(env deviceWiringEnv) (io.Closer, error)
}

func TestOnlineDevicesWiringUDP(t *testing.T) {
	quicRaw := deviceWiringTLSRaw(map[string]any{"network": "udp"})
	ss2022PSK := make([]byte, 16)
	_, _ = rand.Read(ss2022PSK)
	cases := []deviceUDPCase{
		{name: "hysteria2", proto: "hysteria2", uuid: "device-wiring-udp-hy2", raw: quicRaw, openUDP: openDeviceWiringHy2UDP, openTCP: openDeviceWiringHy2},
		{name: "tuic", proto: "tuic", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7301", raw: quicRaw, openUDP: openDeviceWiringTUICUDP, openTCP: openDeviceWiringTUIC},
		{name: "shadowsocks", proto: "shadowsocks", uuid: "device-wiring-udp-ss", hold: true,
			raw: func(*testing.T) map[string]any { return map[string]any{"method": "aes-128-gcm", "network": "udp"} },
			openUDP: openDeviceWiringSSUDP(func(raw net.Conn, user core.User) (net.PacketConn, error) {
				method, err := SS.CreateMethod(context.Background(), "aes-128-gcm", SS.MethodOptions{Password: user.UUID})
				if err != nil {
					return nil, err
				}
				return method.DialPacketConn(raw), nil
			})},
		{name: "shadowsocks2022", proto: "shadowsocks", uuid: "device-wiring-udp-ss2022", hold: true,
			raw: func(*testing.T) map[string]any {
				return map[string]any{"method": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(ss2022PSK), "network": "udp"}
			},
			openUDP: openDeviceWiringSSUDP(func(raw net.Conn, _ core.User) (net.PacketConn, error) {
				method, err := ss2022ref.New("2022-blake3-aes-128-gcm", [][]byte{ss2022PSK}, nil)
				if err != nil {
					return nil, err
				}
				return method.DialPacketConn(raw), nil
			})},
		{name: "socks", proto: "socks", uuid: "device-wiring-udp-socks", openUDP: openDeviceWiringSOCKSUDP, openTCP: lifecycleOpen(dialDeviceWiringSOCKS5)},
		{name: "trojan", proto: "trojan", uuid: "device-wiring-udp-trojan", openUDP: openDeviceWiringTrojanUDP, openTCP: lifecycleOpen(dialLifecycleTrojan)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runDeviceWiringUDP(t, tc) })
	}
}

func runDeviceWiringUDP(t *testing.T, tc deviceUDPCase) {
	tcpEcho := startLifecycleEcho(t, false)
	udpEcho := startUDPWiringEcho(t, tc.hold)
	user := core.User{ID: 7200, UUID: tc.uuid, DeviceLimit: 1}
	raw := map[string]any{}
	if tc.raw != nil {
		raw = tc.raw(t)
	}
	c, port, tag := startLifecycleCore(t, lifecycleProto{name: tc.proto, raw: raw}, []core.User{user})
	adapter := deviceWiringAdapter(t, c, tag)
	env := deviceWiringEnv{t: t, port: port, user: user, target: tcpEcho.addr(), adapter: adapter}
	online := func() []string { return adapter.OnlineIPs()[user.ID] }

	udp, err := tc.openUDP(env, udpEcho)
	if err != nil {
		t.Fatalf("UDP 会话：%v", err)
	}
	defer udp.Close()
	var second io.Closer
	if tc.openTCP != nil {
		second, err = tc.openTCP(env)
	} else {
		second, err = tc.openUDP(env, udpEcho)
	}
	if err != nil {
		t.Fatalf("同 IP 第二条连接被拒（上限 1，同 IP 不该额外占名额）：%v", err)
	}
	defer second.Close()
	if got := online(); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("UDP 会话加第二条连接后在线=%v，期望 [127.0.0.1]", got)
	}

	// 关掉 UDP 会话：另一条还在，用户必须仍在线。
	_ = udp.Close()
	time.Sleep(300 * time.Millisecond)
	if got := online(); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("关掉 UDP 会话后在线=%v，期望仍是 [127.0.0.1]", got)
	}

	// 全部关掉才离线。
	_ = second.Close()
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		ips := online()
		return len(ips) == 0, fmt.Sprintf("在线=%v 会话=%d", ips, liveSessionsOf(adapter))
	}); !ok {
		t.Fatalf("全部关掉后仍在线（UDP 路径漏了 leave？）：%s", why)
	}
}

func deviceWiringAdapter(t *testing.T, c *NativeCore, tag string) Adapter {
	t.Helper()
	in, err := c.getInbound(tag)
	if err != nil {
		t.Fatal(err)
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	return in.adapter
}

// udpWiringEcho 是 UDP 回显上游。hold 为真时收到的包先扣住，release 时才回。
type udpWiringEcho struct {
	pc   *net.UDPConn
	hold bool
	seq  atomic.Int64
	mu   sync.Mutex
	held map[string]net.Addr
	got  chan string
}

func startUDPWiringEcho(t *testing.T, hold bool) *udpWiringEcho {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	e := &udpWiringEcho{pc: pc, hold: hold, held: make(map[string]net.Addr), got: make(chan string, 16)}
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if !hold {
				_, _ = pc.WriteTo(buf[:n], addr)
				continue
			}
			payload := string(buf[:n])
			e.mu.Lock()
			e.held[payload] = addr
			e.mu.Unlock()
			e.got <- payload
		}
	}()
	t.Cleanup(func() { _ = pc.Close() })
	return e
}

func (e *udpWiringEcho) addr() *net.UDPAddr { return e.pc.LocalAddr().(*net.UDPAddr) }

func (e *udpWiringEcho) nextPayload() string {
	return fmt.Sprintf("device-wiring-udp-%d", e.seq.Add(1))
}

// waitHeld 等上游收到 payload（hold 模式）。
func (e *udpWiringEcho) waitHeld(payload string) error {
	deadline := time.After(3 * time.Second)
	for {
		select {
		case got := <-e.got:
			if got == payload {
				return nil
			}
			e.got <- got // 别的会话的包，放回去
			time.Sleep(10 * time.Millisecond)
		case <-deadline:
			return fmt.Errorf("上游 3 秒内没收到 %q", payload)
		}
	}
}

func (e *udpWiringEcho) release(payload string) {
	e.mu.Lock()
	addr := e.held[payload]
	delete(e.held, payload)
	e.mu.Unlock()
	if addr != nil {
		_, _ = e.pc.WriteTo([]byte(payload), addr)
	}
}

// deviceWiringUDPEcho 经 pc 发一包到 target 并等回显。
func deviceWiringUDPEcho(pc net.PacketConn, target net.Addr, payload string) error {
	if _, err := pc.WriteTo([]byte(payload), target); err != nil {
		return err
	}
	_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer pc.SetReadDeadline(time.Time{})
	buf := make([]byte, 2048)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		return err
	}
	if string(buf[:n]) != payload {
		return fmt.Errorf("udp echo=%q", buf[:n])
	}
	return nil
}

func openDeviceWiringHy2UDP(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error) {
	ctx := context.Background()
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddrHostPort("127.0.0.1", uint16(env.port)),
		Password:      env.user.UUID, TLSConfig: deviceWiringQUICClientTLS(),
	})
	if err != nil {
		return nil, err
	}
	closeClient := func() error { return client.CloseWithError(nil) }
	udp, err := client.ListenPacket(ctx)
	if err != nil {
		_ = closeClient()
		return nil, err
	}
	if err := deviceWiringUDPEcho(udp, M.SocksaddrFromNet(echo.addr()), echo.nextPayload()); err != nil {
		_ = udp.Close()
		_ = closeClient()
		return nil, err
	}
	// Hysteria2 的 UDP 会话没有关闭报文，服务端靠 QUIC 连接断开或空闲超时收尾：关整个客户端。
	return streamThenClient(udp, closeClient), nil
}

func openDeviceWiringTUICUDP(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error) {
	ctx := context.Background()
	parsed, err := gofrsuuid.FromString(env.user.UUID)
	if err != nil {
		return nil, err
	}
	client, err := tuic.NewClient(tuic.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddrHostPort("127.0.0.1", uint16(env.port)),
		TLSConfig:     deviceWiringQUICClientTLS(), UUID: [16]byte(parsed), Password: env.user.UUID,
	})
	if err != nil {
		return nil, err
	}
	closeClient := func() error { return client.CloseWithError(nil) }
	udp, err := client.ListenPacket(ctx)
	if err != nil {
		_ = closeClient()
		return nil, err
	}
	if err := deviceWiringUDPEcho(udp, M.SocksaddrFromNet(echo.addr()), echo.nextPayload()); err != nil {
		_ = udp.Close()
		_ = closeClient()
		return nil, err
	}
	return streamThenClient(udp, closeClient), nil
}

// openDeviceWiringSSUDP：发一包到扣包的上游，确认上游收到即会话在途；关会话时
// 让上游回包、客户端收到，服务端这一包的处理随之结束。
func openDeviceWiringSSUDP(dial func(raw net.Conn, user core.User) (net.PacketConn, error)) func(deviceWiringEnv, *udpWiringEcho) (io.Closer, error) {
	return func(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error) {
		raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: env.port})
		if err != nil {
			return nil, err
		}
		pc, err := dial(raw, env.user)
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		payload := echo.nextPayload()
		if _, err := pc.WriteTo([]byte(payload), echo.addr()); err != nil {
			_ = raw.Close()
			return nil, err
		}
		if err := echo.waitHeld(payload); err != nil {
			_ = raw.Close()
			return nil, err
		}
		var once sync.Once
		return closerFunc(func() error {
			var err error
			once.Do(func() {
				defer raw.Close()
				echo.release(payload)
				_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
				buf := make([]byte, 2048)
				var n int
				if n, _, err = pc.ReadFrom(buf); err == nil && string(buf[:n]) != payload {
					err = fmt.Errorf("udp echo=%q", buf[:n])
				}
			})
			return err
		}), nil
	}
}

func openDeviceWiringSOCKSUDP(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error) {
	control, reply, err := socks5DeviceWiringRequest(env.port, env.user, 3, net.IPv4zero, 0)
	if err != nil {
		return nil, err
	}
	relay := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(reply[8])<<8 | int(reply[9])}
	udp, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	closeAll := closerFunc(func() error { return errors.Join(udp.Close(), control.Close()) })
	payload := echo.nextPayload()
	if _, err := udp.Write(socksUDPDatagram(echo.addr(), []byte(payload))); err != nil {
		_ = closeAll.Close()
		return nil, err
	}
	_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, err := udp.Read(buf)
	if err != nil || n < 10 || string(buf[10:n]) != payload {
		_ = closeAll.Close()
		return nil, fmt.Errorf("socks udp echo=%q err=%v", buf[:n], err)
	}
	// 关控制连接即结束这次 UDP ASSOCIATE。
	return closeAll, nil
}

func openDeviceWiringTrojanUDP(env deviceWiringEnv, echo *udpWiringEcho) (io.Closer, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(env.port)), 3*time.Second)
	if err != nil {
		return nil, err
	}
	target := echo.addr()
	header := []byte(trojanPasswordProof(env.user.UUID) + "\r\n")
	header = append(header, trojanCommandUDP, 1)
	header = append(header, target.IP.To4()...)
	header = append(header, byte(target.Port>>8), byte(target.Port), '\r', '\n')
	if _, err := conn.Write(header); err != nil {
		_ = conn.Close()
		return nil, err
	}
	payload := echo.nextPayload()
	destination := vlessDestination{IP: netip.MustParseAddr("127.0.0.1"), Host: "127.0.0.1", Port: uint16(target.Port)}
	if err := writeTrojanUDPPacket(conn, destination, []byte(payload)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, got, err := readTrojanUDPPacket(conn, make([]byte, trojanUDPMaxPacket))
	if err != nil || string(got) != payload {
		_ = conn.Close()
		return nil, fmt.Errorf("trojan udp echo=%q err=%v", got, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn, nil
}

// hy2 / TUIC 每用户 UDP 会话上限：到上限的新会话被拒，已有会话不受影响；
// 关掉一个才腾出名额。上限在测试里调成 1。
func TestQUICUDPSessionQuotaWiring(t *testing.T) {
	quicRaw := deviceWiringTLSRaw(map[string]any{"network": "udp"})
	for _, tc := range []deviceUDPCase{
		{name: "hysteria2", proto: "hysteria2", uuid: "device-wiring-quota-hy2", raw: quicRaw, openUDP: openDeviceWiringHy2UDP},
		{name: "tuic", proto: "tuic", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7401", raw: quicRaw, openUDP: openDeviceWiringTUICUDP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			udpEcho := startUDPWiringEcho(t, false)
			user := core.User{ID: 7300, UUID: tc.uuid}
			c, port, tag := startLifecycleCore(t, lifecycleProto{name: tc.proto, raw: tc.raw(t)}, []core.User{user})
			adapter := deviceWiringAdapter(t, c, tag)
			var quota *udpSessionQuota
			switch a := adapter.(type) {
			case *hysteria2Adapter:
				quota = &a.udpQuota
			case *tuicAdapter:
				quota = &a.udpQuota
			default:
				t.Fatalf("adapter=%T", adapter)
			}
			quota.mu.Lock()
			quota.limit = 1
			quota.mu.Unlock()
			env := deviceWiringEnv{t: t, port: port, user: user, adapter: adapter}

			first, err := tc.openUDP(env, udpEcho)
			if err != nil {
				t.Fatalf("第一个 UDP 会话：%v", err)
			}
			defer first.Close()
			if extra, err := tc.openUDP(env, udpEcho); err == nil {
				_ = extra.Close()
				t.Fatal("到上限后新的 UDP 会话仍被放行")
			}
			_ = first.Close()
			if ok, why := waitFor(3*time.Second, func() (bool, string) {
				quota.mu.Lock()
				defer quota.mu.Unlock()
				return len(quota.byUser) == 0, fmt.Sprintf("在途=%v", quota.byUser)
			}); !ok {
				t.Fatalf("关掉会话后名额没归还：%s", why)
			}
			again, err := tc.openUDP(env, udpEcho)
			if err != nil {
				t.Fatalf("名额归还后新会话仍被拒：%v", err)
			}
			_ = again.Close()
		})
	}
}
