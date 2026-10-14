//go:build !race

package kernel

// sing-box 的 gRPC lite 客户端自带数据竞争（transport/v2raygrpclite/conn.go），
// 与 interop 门里的外部客户端同理不能进 race 套件；非 race 的全量测试照常跑它。

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2raygrpclite"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 用 sing-box 的 gRPC 客户端（gun lite，与 Xray / mihomo 同一线格式）连 NativeCore
// 的 gRPC 入站：入站要求 Host=node.example.com，而客户端的 :authority 带着端口。
// 以前这里先因 Host 带端口 404，修掉之后又因为没解 Hunk 让协议层读到 0x0a。
func TestNativeGRPCInteropWithSingBoxGunClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	echo := func(_ context.Context, conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 64<<10)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if _, werr := conn.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	server, err := serveNativeGRPC(ln, "/svc/Tun", "node.example.com", 16<<20, true, echo)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	// 拨号永远到本地监听，但 :authority 是 node.example.com:<端口>
	dialer := fixedDialer{target: ln.Addr().String()}
	client := v2raygrpclite.NewClient(ctx, dialer, M.ParseSocksaddrHostPort("node.example.com", port),
		option.V2RayGRPCOptions{ServiceName: "svc"}, nil)
	conn, err := client.DialContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, payload := range [][]byte{[]byte("\x00vless-like-first-byte"), bytes.Repeat([]byte("x"), 40000)} {
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("echo mismatch: got %d bytes", len(got))
		}
	}
}

type fixedDialer struct{ target string }

func (d fixedDialer) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, N.NetworkTCP, d.target)
}

func (d fixedDialer) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, net.ErrClosed
}
