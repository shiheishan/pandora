//go:build interop

package kernel

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"

	"github.com/aegispanel/nodeagent/core"
)

// AnyTLS v2 的 cmdSYNACK（规范 anytls-go docs/protocol.md「cmdSYNACK」）：客户端
// 宣告 v>=2 后，在复用的会话上开 sid>=2 的流时起 3 秒定时器，收不到 SYNACK 就关
// 整条会话。客户端一条会话同一时刻只承载一条流，流关闭后会话回到空闲池，所以
// 复用会话上的下一条流一定是 sid>=2。以下用例都先在会话上开一条流再关掉，第二条
// 流就落在复用会话上；拨号计数证明确实复用了，没有另建会话。
//
// 这组用例挂在 TestAnyTLSNativeClientTCPAndUOTUDP 下随 CI 的 interop 门跑。
//
// 两组先后跑：大流量会占满 CPU，不和按时限判定的往返、失败用例挤在一起。
func runAnyTLSSynAckGroup(t *testing.T) {
	t.Run("timing", func(t *testing.T) {
		t.Run("reused-stream-survives", func(t *testing.T) { t.Parallel(); runAnyTLSReusedStreamSurvives(t) })
		t.Run("dial-failure-reported", func(t *testing.T) { t.Parallel(); runAnyTLSDialFailureReported(t) })
		t.Run("reused-uot-survives", func(t *testing.T) { t.Parallel(); runAnyTLSReusedUOTSurvives(t) })
	})
	t.Run("bulk", func(t *testing.T) {
		t.Run("reused-up", func(t *testing.T) { t.Parallel(); runAnyTLSReusedBulk(t, "up") })
		t.Run("reused-down", func(t *testing.T) { t.Parallel(); runAnyTLSReusedBulk(t, "down") })
	})
}

// anyTLSReuseFixture 起一个 AnyTLS 入站和 sing-anytls 客户端，并数客户端拨了几次
// 外层 TCP（= 建了几条会话）。
type anyTLSReuseFixture struct {
	client *anytls.Client
	dials  atomic.Int32
}

func newAnyTLSReuseFixture(t *testing.T, userID int64) *anyTLSReuseFixture {
	t.Helper()
	user := core.User{ID: userID, UUID: "anytls-synack-secret-" + strconv.FormatInt(userID, 10)}
	_, port, _ := startLifecycleCore(t, lifecycleProto{name: "anytls", raw: map[string]any{}}, []core.User{user})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	f := &anyTLSReuseFixture{}
	dialOut := func(ctx context.Context) (net.Conn, error) {
		f.dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: user.UUID, DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	f.client = client
	return f
}

// warmSession 在会话上开第一条流、往返一次后关掉，会话回到客户端的空闲池。
//
// 关掉后等一会儿再开第二条流：客户端的 3 秒定时器是整条会话一个、不分 sid，
// 收到任何 SYNACK 都会撤销。服务端若在第一条流收尾时才补发 SYNACK（例如漏了
// 拨号成功的那次、只剩出口处的失败回报），这个迟到的帧要在第二条流的定时器
// 起来之前到达，否则会把它撤掉、掩盖回归。
func (f *anyTLSReuseFixture) warmSession(t *testing.T, sink M.Socksaddr) {
	t.Helper()
	_ = openEcho(t, f, sink, false).Close()
	time.Sleep(300 * time.Millisecond)
}

// openReused 在复用会话上开下一条流，并确认没有另拨外层连接。
func (f *anyTLSReuseFixture) openReused(t *testing.T, target M.Socksaddr) net.Conn {
	t.Helper()
	stream, err := f.client.CreateProxy(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if n := f.dials.Load(); n != 1 {
		t.Fatalf("客户端拨了 %d 次外层连接，第二条流没有落在复用会话上", n)
	}
	return stream
}

func anyTLSPing(c net.Conn) error {
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte{'x'}); err != nil {
		return err
	}
	one := make([]byte, 1)
	_, err := io.ReadFull(c, one)
	return err
}

// startAnyTLSSink 起一个回环目标：首字节 'e' 回显，'u' 吞掉上行，'d' 持续下发直到对端关。
func startAnyTLSSink(t *testing.T) M.Socksaddr {
	t.Helper()
	sink, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	go func() {
		for {
			c, err := sink.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var mode [1]byte
				if _, err := io.ReadFull(c, mode[:]); err != nil {
					return
				}
				switch mode[0] {
				case 'e':
					_, _ = c.Write(mode[:])
					_, _ = io.Copy(c, c)
				case 'u':
					_, _ = io.Copy(io.Discard, c)
				case 'd':
					chunk := make([]byte, 32<<10)
					for {
						if _, err := c.Write(chunk); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	return M.SocksaddrFromNet(sink.Addr())
}

// 回显端要先收一个模式字节；ping 的 'x' 之前补一个 'e'。
func openEcho(t *testing.T, f *anyTLSReuseFixture, sink M.Socksaddr, reused bool) net.Conn {
	t.Helper()
	var stream net.Conn
	if reused {
		stream = f.openReused(t, sink)
	} else {
		var err error
		if stream, err = f.client.CreateProxy(context.Background(), sink); err != nil {
			t.Fatal(err)
		}
	}
	if err := anyTLSPing(withModeByte(stream, 'e')); err != nil {
		t.Fatalf("回显流建立失败：%v", err)
	}
	return stream
}

// withModeByte 让下一次 Write 前面带上模式字节，只带一次。
func withModeByte(c net.Conn, mode byte) net.Conn { return &modeConn{Conn: c, mode: mode} }

type modeConn struct {
	net.Conn
	mode byte
	sent bool
}

func (m *modeConn) Write(b []byte) (int, error) {
	if m.sent {
		return m.Conn.Write(b)
	}
	m.sent = true
	if _, err := m.Conn.Write(append([]byte{m.mode}, b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}

// 复用会话上的第二条流每 250ms 往返一次，持续 8 秒都可用（修前第 3 秒整条会话被
// 客户端关掉：use of closed network connection）。
func runAnyTLSReusedStreamSurvives(t *testing.T) {
	sink := startAnyTLSSink(t)
	f := newAnyTLSReuseFixture(t, 6301)
	f.warmSession(t, sink)
	s2 := openEcho(t, f, sink, true)
	defer s2.Close()
	start := time.Now()
	for time.Since(start) < 8*time.Second {
		if err := anyTLSPing(s2); err != nil {
			t.Fatalf("复用会话上的流在 %s 断开：%v", time.Since(start).Round(100*time.Millisecond), err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// 复用会话上的单向大流量持续 6 秒不断（iperf3 的数据流就是这个形状：修前第 3 秒
// 断开，报 control socket has closed unexpectedly）。
func runAnyTLSReusedBulk(t *testing.T, dir string) {
	sink := startAnyTLSSink(t)
	f := newAnyTLSReuseFixture(t, map[string]int64{"up": 6302, "down": 6303}[dir])
	f.warmSession(t, sink)
	data := f.openReused(t, sink)
	defer data.Close()
	const span = 6 * time.Second
	start := time.Now()
	var moved int64
	chunk := make([]byte, 32<<10)
	if dir == "up" {
		if _, err := data.Write([]byte{'u'}); err != nil {
			t.Fatal(err)
		}
		for time.Since(start) < span {
			_ = data.SetWriteDeadline(time.Now().Add(2 * time.Second))
			n, err := data.Write(chunk)
			moved += int64(n)
			if err != nil {
				t.Fatalf("上行第 %s 断开（已发 %d 字节）：%v", time.Since(start).Round(100*time.Millisecond), moved, err)
			}
		}
	} else {
		if _, err := data.Write([]byte{'d'}); err != nil {
			t.Fatal(err)
		}
		for time.Since(start) < span {
			_ = data.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := data.Read(chunk)
			moved += int64(n)
			if err != nil {
				t.Fatalf("下行第 %s 断开（已收 %d 字节）：%v", time.Since(start).Round(100*time.Millisecond), moved, err)
			}
		}
	}
	t.Logf("%s 复用会话 %s 内传了 %d MB", dir, span, moved>>20)
}

// 拨号失败：客户端立刻从 SYNACK 收到失败（不是等 3 秒整条会话被关），文字是中性
// 的、不带目标地址与内部原因；同一会话随后还能照常开流、并撑过 3 秒定时器。
func runAnyTLSDialFailureReported(t *testing.T) {
	sink := startAnyTLSSink(t)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := M.SocksaddrFromNet(dead.Addr())
	_ = dead.Close() // 端口关掉，拨号立刻被拒
	f := newAnyTLSReuseFixture(t, 6304)
	f.warmSession(t, sink)

	failed := f.openReused(t, deadAddr)
	start := time.Now()
	_ = failed.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = failed.Read(make([]byte, 1))
	elapsed := time.Since(start)
	_ = failed.Close()
	if err == nil {
		t.Fatal("拨号失败的流读到了数据")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("拨号失败 5 秒内没有收到任何结果：%v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("拨号失败用了 %s 才报给客户端，应当立刻报", elapsed)
	}
	want := "remote: " + errAnyTLSStreamRefused.Error()
	if err.Error() != want {
		t.Fatalf("客户端收到的失败是 %q，应为经 SYNACK 送达的中性文字 %q", err.Error(), want)
	}
	for _, leak := range []string{strconv.Itoa(int(deadAddr.Port)), "127.0.0.1", "dial", "anytls", "pandora", "pdnd"} {
		if strings.Contains(strings.ToLower(err.Error()), leak) {
			t.Fatalf("失败文字 %q 含内部信息 %q", err.Error(), leak)
		}
	}

	// 同一会话再开一条流：仍复用（拨号计数不变），并撑过 3 秒定时器。
	s3 := openEcho(t, f, sink, true)
	defer s3.Close()
	time.Sleep(3500 * time.Millisecond)
	if err := anyTLSPing(s3); err != nil {
		t.Fatalf("拨号失败之后，同一会话上的新流过了 3 秒不可用：%v", err)
	}
}

// UoT 没有拨号，进入前就回成功：复用会话上的 UoT 流过了 3 秒仍能收发。
func runAnyTLSReusedUOTSurvives(t *testing.T) {
	sink := startAnyTLSSink(t)
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := echo.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(b[:n], from)
		}
	}()
	target := M.SocksaddrFromNet(echo.LocalAddr())
	f := newAnyTLSReuseFixture(t, 6305)
	f.warmSession(t, sink)
	proxy := f.openReused(t, M.Socksaddr{Fqdn: uot.MagicAddress})
	pc, err := (&uot.Client{Version: uot.Version}).DialConn(proxy, false, target)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	roundTrip := func(label string) {
		t.Helper()
		if _, err := pc.WriteTo([]byte(label), target); err != nil {
			t.Fatalf("%s 写失败：%v", label, err)
		}
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		got := make([]byte, 64)
		n, _, err := pc.ReadFrom(got)
		if err != nil || string(got[:n]) != label {
			t.Fatalf("%s 回显=%q err=%v", label, got[:n], err)
		}
	}
	roundTrip("uot-first")
	time.Sleep(3500 * time.Millisecond)
	roundTrip("uot-after-3s")
}
