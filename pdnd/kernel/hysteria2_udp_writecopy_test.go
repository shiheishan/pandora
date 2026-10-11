package kernel

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	"github.com/gofrs/uuid/v5"
	hy2 "github.com/sagernet/sing-quic/hysteria2"
	tuic "github.com/sagernet/sing-quic/tuic"
	M "github.com/sagernet/sing/common/metadata"
)

// 下行缓冲跨会话复用的前提（规则文件 pdnd-kernel「共享缓冲的前提」）：nativewire
// hy2 / TUIC 的 WritePacket 返回前同步编码并复制，返回后不再引用负载。
//
// 上游替身在 WritePacket 返回、转发回来再读下一包时，先把整个收包缓冲涂成 0xEE；
// 客户端收到的必须仍是原文。WritePacket 若改成异步持有负载（排队、延后编码），
// 客户端会收到被涂掉的字节。小包、贴 MTU 的包与要分片的包各一种。

// scribbleUpstream 是上游 socket 的替身：WriteTo 收下的包原样排队作回包，ReadFrom
// 先涂掉调用方的整个缓冲再交出下一个回包。它只实现 net.PacketConn，转发走逐包下行，
// 每会话一个收包缓冲反复使用，涂改正好落在刚写回过的那块内存上。
type scribbleUpstream struct {
	replies chan []byte
	closed  chan struct{}
	once    sync.Once
	from    *net.UDPAddr
}

func newScribbleUpstream() *scribbleUpstream {
	return &scribbleUpstream{replies: make(chan []byte, 64), closed: make(chan struct{}), from: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 7), Port: 53}}
}

func (u *scribbleUpstream) ReadFrom(p []byte) (int, net.Addr, error) {
	for i := range p {
		p[i] = 0xEE
	}
	select {
	case reply := <-u.replies:
		return copy(p, reply), u.from, nil
	case <-u.closed:
		return 0, nil, net.ErrClosed
	}
}

func (u *scribbleUpstream) WriteTo(p []byte, _ net.Addr) (int, error) {
	select {
	case u.replies <- bytes.Clone(p):
	case <-u.closed:
		return 0, net.ErrClosed
	}
	return len(p), nil
}

func (u *scribbleUpstream) stop() error  { u.once.Do(func() { close(u.closed) }); return nil }
func (u *scribbleUpstream) Close() error { return u.stop() }
func (u *scribbleUpstream) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}
func (u *scribbleUpstream) SetWriteDeadline(time.Time) error { return nil }
func (u *scribbleUpstream) SetDeadline(t time.Time) error    { return u.SetReadDeadline(t) }

// SetReadDeadline 只认「立即」：转发收尾时用它打断阻塞读。
func (u *scribbleUpstream) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		return u.stop()
	}
	return nil
}

type scribblePlane struct{}

func (scribblePlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, fmt.Errorf("no tcp")
}

func (scribblePlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return newScribbleUpstream(), nil
}

// echoThroughScribble 经隧道逐个发出负载，核对回包逐字节等于原文。
func echoThroughScribble(t *testing.T, udp net.PacketConn) {
	t.Helper()
	target := M.ParseSocksaddr("192.0.2.7:53")
	reply := make([]byte, 64<<10)
	for i, size := range []int{1, 100, 1100, 3000, 1200, 64} {
		payload := bytes.Repeat([]byte{byte(0x10 + i)}, size)
		if _, err := udp.WriteTo(payload, target); err != nil {
			t.Fatal(err)
		}
		_ = udp.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := udp.ReadFrom(reply)
		if err != nil {
			t.Fatalf("第 %d 个包（%d 字节）没有回包：%v", i, size, err)
		}
		if !bytes.Equal(reply[:n], payload) {
			t.Fatalf("第 %d 个包（%d 字节）回包被改写：收到 %d 字节，前几个 %x", i, size, n, reply[:min(n, 8)])
		}
	}
}

func TestHysteria2WritePacketCopiesBeforeReturn(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	probe, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.LocalAddr().(*net.UDPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "hysteria2", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"cert_path": certPath, "key_path": keyPath, "network": "udp",
	}}}
	adapter, err := newHysteria2Adapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: scribblePlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 31, UUID: "scribble-secret"}}); err != nil {
		t.Fatal(err)
	}
	client, err := hy2.NewClient(hy2.ClientOptions{
		Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
		ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)), Password: "scribble-secret",
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
	echoThroughScribble(t, udp)
}

func TestTUICWritePacketCopiesBeforeReturn(t *testing.T) {
	for _, mode := range []string{"native", "quic"} {
		t.Run(mode, func(t *testing.T) {
			certPath, keyPath := testXHTTPServerCertFiles(t)
			probe, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.LocalAddr().(*net.UDPAddr).Port
			_ = probe.Close()
			const userID = "0b6c0f3e-2a55-4f7e-9d0e-6a51c2e3d4f1"
			spec := InboundSpec{Config: core.InboundConfig{Protocol: "tuic", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
				"cert_path": certPath, "key_path": keyPath, "network": "udp",
			}}}
			adapter, err := newTUICAdapter(spec)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: scribblePlane{}}); err != nil {
				t.Fatal(err)
			}
			defer adapter.Close()
			if err := adapter.AddUsers([]core.User{{ID: 32, UUID: userID}}); err != nil {
				t.Fatal(err)
			}
			parsed, err := uuid.FromString(userID)
			if err != nil {
				t.Fatal(err)
			}
			client, err := tuic.NewClient(tuic.ClientOptions{
				Context: ctx, Dialer: outbound.NewDirect("test", outbound.StrategyPreferIPv4, nil),
				ServerAddress: M.ParseSocksaddr("127.0.0.1:" + strconv.Itoa(port)),
				TLSConfig:     &hysteria2TLSConfig{std: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost", NextProtos: []string{"h3"}}},
				UUID:          [16]byte(parsed), Password: userID, UDPStream: mode == "quic",
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
			echoThroughScribble(t, udp)
		})
	}
}
