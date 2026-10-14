//go:build !race

package kernel

// xray-core 的 gun HunkConn 与 interop 门里的外部客户端同理，不进 race 套件；
// 非 race 的全量测试照常跑它。

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/xtls/xray-core/transport/internet/grpc/encoding"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// 用 xray-core 的 gun 客户端（与 sing-box / mihomo 同一线格式）连 NativeCore
// 的 gRPC 入站：入站要求 Host=node.example.com，而客户端的 :authority 带着端口。
// 以前这里先因 Host 带端口 404，修掉之后又因为没解 Hunk 让协议层读到 0x0a。
func TestNativeGRPCInteropWithXrayGunClient(t *testing.T) {
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
	port := ln.Addr().(*net.TCPAddr).Port
	authority := "node.example.com:" + strconv.Itoa(port)
	dialTarget := ln.Addr().String()
	cc, err := grpc.NewClient("passthrough:///"+authority,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority(authority),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", dialTarget)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	client := encoding.NewGRPCServiceClient(cc)
	tun, err := client.(encoding.GRPCServiceClientX).TunCustomName(ctx, "svc", "Tun")
	if err != nil {
		t.Fatal(err)
	}
	conn := encoding.NewHunkConn(tun, nil)
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
