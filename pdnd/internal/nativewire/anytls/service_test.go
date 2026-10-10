package anytls

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// echoHandler 回成功后把子流原样回显，并数被调了几次。
type echoHandler struct{ calls atomic.Int32 }

func (h *echoHandler) NewConnectionEx(_ context.Context, conn net.Conn, _ M.Socksaddr, _ M.Socksaddr, _ N.CloseHandlerFunc) {
	h.calls.Add(1)
	defer conn.Close()
	_ = N.ReportConnHandshakeSuccess(conn, nil)
	_, _ = io.Copy(conn, conn)
}

// 子流的目标地址读不出来（畸形地址）：服务端立刻经 cmdSYNACK 回中性失败并关流，
// 不交给 handler；同一会话随后还能照常开新流。上游在这里直接 return，客户端要
// 等 3 秒定时器关掉整条会话。
func TestServiceMalformedDestinationRefusesStream(t *testing.T) {
	handler := &echoHandler{}
	service, err := NewService(ServiceConfig{
		PaddingScheme: []byte("stop=1\n0=0-0"), Users: []User{{Name: "u", Password: "secret"}},
		Handler: handler, Logger: logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = service.NewConnection(ctx, conn, M.SocksaddrFromNet(conn.RemoteAddr()), nil)
			}()
		}
	}()
	var dials atomic.Int32
	client, err := NewClient(ctx, ClientConfig{Password: "secret", Logger: logger.NOP(),
		DialOut: func(ctx context.Context) (net.Conn, error) {
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	target := M.ParseSocksaddr("127.0.0.1:9")
	echo := func(label string) {
		t.Helper()
		stream, err := client.CreateProxy(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := stream.Write([]byte(label)); err != nil {
			t.Fatalf("%s 写：%v", label, err)
		}
		got := make([]byte, len(label))
		if _, err := io.ReadFull(stream, got); err != nil || string(got) != label {
			t.Fatalf("%s 回显=%q err=%v", label, got, err)
		}
	}
	echo("first")
	time.Sleep(300 * time.Millisecond)

	// 复用会话上开一条流，地址类型字节 0xFF 不存在。
	raw, err := client.sessionClient.CreateStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Write([]byte{0xFF, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = raw.Read(make([]byte, 1))
	elapsed := time.Since(start)
	_ = raw.Close()
	if want := "remote: " + errStreamRefused.Error(); err == nil || err.Error() != want {
		t.Fatalf("畸形地址的流读到 err=%v，应为经 SYNACK 送达的 %q", err, want)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("畸形地址用了 %s 才报给客户端", elapsed)
	}
	if n := handler.calls.Load(); n != 1 {
		t.Fatalf("handler 被调 %d 次，畸形地址不应交给 handler", n)
	}
	time.Sleep(500 * time.Millisecond)
	echo("after-malformed")
	if n := dials.Load(); n != 1 {
		t.Fatalf("客户端拨了 %d 次外层连接，应一直复用同一会话", n)
	}
}
