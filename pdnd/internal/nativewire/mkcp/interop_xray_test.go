//go:build interop

package mkcp

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	xraykcp "github.com/xtls/xray-core/transport/internet/kcp"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// 与 xray 的连接级互通。
//
// segment_xray_test.go 已经证明单个段的编解码双向一致，但那只到线格式为止。
// 真正要回答的问题是整条连接能不能跑起来：会话建立、序号推进、ACK、重传、
// 关闭握手——任何一环理解错了，段格式再对也传不了数据。
//
// 这是 opt-in 的：xray 只在 interop 标签下引入。

// xrayStreamSettings 构造一份 mKCP 的流设置，全部走 xray 的默认值。
//
// 刻意不调任何参数：默认值才是真实客户端会用的。我们这边要去迁就它，
// 而不是反过来把对端调到我们舒服的位置。
func xrayStreamSettings() *internet.MemoryStreamConfig {
	return &internet.MemoryStreamConfig{
		ProtocolName:     "mkcp",
		ProtocolSettings: &xraykcp.Config{},
	}
}

// 方向一：xray 当客户端拨过来，我们当服务端。
//
// 这个方向验证我们发出去的东西 xray 认得——包括 ACK 的时机和内容，
// 那是 xray 决定要不要继续发下一批数据的依据。
func TestInteropXrayDialsPandoraListener(t *testing.T) {
	l, err := Listen("udp", "127.0.0.1:0", testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()

	addr := l.Addr().(*net.UDPAddr)
	dest := xnet.UDPDestination(xnet.IPAddress(addr.IP), xnet.Port(addr.Port))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := xraykcp.DialKCP(ctx, dest, xrayStreamSettings())
	if err != nil {
		t.Fatalf("xray 拨号失败：%v", err)
	}
	defer client.Close()

	assertEcho(t, client, []byte("xray client to pandora server"))
}

// 方向二：我们当客户端，xray 当服务端。
//
// 这个方向验证我们理解 xray 的行为——它什么时候回 ACK、怎么划分窗口、
// 关闭时发什么。方向一跑通不代表这个方向也通：两侧的代码路径完全不同。
func TestInteropPandoraDialsXrayListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accepted := make(chan stat.Connection, 4)
	listener, err := xraykcp.NewListener(ctx, xnet.LocalHostIP, xnet.Port(0),
		xrayStreamSettings(), func(conn stat.Connection) { accepted <- conn })
	if err != nil {
		t.Fatalf("xray 监听失败：%v", err)
	}
	defer listener.Close()

	go func() {
		for conn := range accepted {
			go func(c stat.Connection) { defer c.Close(); _, _ = io.Copy(c, c) }(conn)
		}
	}()

	client, err := Dial("udp", listener.Addr().String(), testConfig())
	if err != nil {
		t.Fatalf("拨号 xray 失败：%v", err)
	}
	defer client.Close()

	assertEcho(t, client, []byte("pandora client to xray server"))
}

// 大载荷跨实现传输。小消息一个段就装下了，走不到窗口流控和重组——
// 而那正是两边最容易理解不一致的地方。
func TestInteropLargePayloadBothDirections(t *testing.T) {
	if testing.Short() {
		t.Skip("跨实现大载荷传输耗时")
	}
	payload := make([]byte, 512*1024)
	for i := range payload {
		payload[i] = byte(i * 7)
	}

	t.Run("xray 发给我们", func(t *testing.T) {
		l, err := Listen("udp", "127.0.0.1:0", testConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		go func() {
			for {
				conn, err := l.Accept()
				if err != nil {
					return
				}
				go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
			}
		}()

		addr := l.Addr().(*net.UDPAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		client, err := xraykcp.DialKCP(ctx,
			xnet.UDPDestination(xnet.IPAddress(addr.IP), xnet.Port(addr.Port)),
			xrayStreamSettings())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		assertEcho(t, client, payload)
	})

	t.Run("我们发给 xray", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		accepted := make(chan stat.Connection, 4)
		listener, err := xraykcp.NewListener(ctx, xnet.LocalHostIP, xnet.Port(0),
			xrayStreamSettings(), func(conn stat.Connection) { accepted <- conn })
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		go func() {
			for conn := range accepted {
				go func(c stat.Connection) { defer c.Close(); _, _ = io.Copy(c, c) }(conn)
			}
		}()

		client, err := Dial("udp", listener.Addr().String(), testConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		assertEcho(t, client, payload)
	})
}

// assertEcho 写一段数据并要求原样读回。
func assertEcho(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	errc := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		errc <- err
	}()

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("读回声失败（收到 %d/%d 字节）：%v", len(got), len(payload), err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if !bytes.Equal(got, payload) {
		for i := range got {
			if got[i] != payload[i] {
				t.Fatalf("第 %d 字节起内容不一致：收到 %#x，期望 %#x", i, got[i], payload[i])
			}
		}
	}
}
