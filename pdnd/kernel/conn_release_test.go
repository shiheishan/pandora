package kernel

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 客户端断开即释放（10 万连接实测的会话泄漏）：N 条经代理连到回显上游的连接，
// 客户端全部关闭后 2 秒内，pdnd 的会话数、goroutine、fd 回到基线，在线 IP 不再
// 上报这些用户。两种上游各跑一遍：
//   - 读到 EOF 就关的回显：靠半关闭能传到上游（出站租约连接的 CloseWrite）。
//   - 读到 EOF 也不关的回显：靠单向收尾计时（默认 1 秒）兜底。
func TestClientCloseReleasesSessions(t *testing.T) {
	for _, p := range lifecycleProtos() {
		for _, hold := range []bool{false, true} {
			name := p.name + "/close-on-eof"
			if hold {
				name = p.name + "/ignore-half-close"
			}
			t.Run(name, func(t *testing.T) { runClientCloseRelease(t, p, hold) })
		}
	}
}

func runClientCloseRelease(t *testing.T, p lifecycleProto, holdAfterEOF bool) {
	const n = 40
	echo := startLifecycleEcho(t, holdAfterEOF)
	users := make([]core.User, 4)
	for i := range users {
		users[i] = p.user(i)
	}
	c, port, tag := startLifecycleCore(t, p, users)
	time.Sleep(50 * time.Millisecond)
	baseG, baseFD := goroutines(), openFDs()

	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := p.dial(port, users[i%len(users)], echo.addr())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if err := echoOnce(conn, fmt.Sprintf("hello-%d", i)); err != nil {
			t.Fatalf("echo %d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	if got := c.liveSessionsForTest(tag); got != n {
		t.Fatalf("在途会话=%d，期望 %d", got, n)
	}
	if online := c.OnlineIPs(tag); len(online) == 0 {
		t.Fatal("连接建立后在线 IP 应非空")
	}

	for _, conn := range conns {
		_ = conn.Close()
	}
	ok, why := waitFor(2*time.Second, func() (bool, string) {
		live := c.liveSessionsForTest(tag)
		online := len(c.OnlineIPs(tag))
		g := goroutines()
		fds := openFDs()
		upstream := echo.active.Load()
		// 上游不理会半关闭时，回显那一侧（同进程）的连接会一直开着：fd 与 goroutine
		// 按它的份额放宽，pdnd 自己那一侧必须全部释放（会话数、在线 IP）。
		slackG, slackFD := 4, 4
		if holdAfterEOF {
			slackG += int(upstream)
			slackFD += int(upstream)
		}
		done := live == 0 && online == 0 && g <= baseG+slackG && fds <= baseFD+slackFD
		if !holdAfterEOF {
			done = done && upstream == 0
		}
		return done, fmt.Sprintf("会话=%d 在线用户=%d goroutine=%d(基线 %d) fd=%d(基线 %d) 上游连接=%d",
			live, online, g, baseG, fds, baseFD, upstream)
	})
	if !ok {
		t.Fatalf("客户端全部关闭 2 秒后仍未释放：%s", why)
	}
}

// 上游先关（下行 EOF）时，半关闭要传到客户端：客户端读到 EOF，而不是一直挂着。
func TestUpstreamCloseReachesClient(t *testing.T) {
	for _, p := range lifecycleProtos() {
		t.Run(p.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						buf := make([]byte, 64)
						n, _ := conn.Read(buf)
						_, _ = conn.Write(buf[:n])
						_ = conn.Close() // 回一次就关
					}()
				}
			}()
			user := p.user(0)
			c, port, tag := startLifecycleCore(t, p, []core.User{user})
			conn, err := p.dial(port, user, ln.Addr().(*net.TCPAddr))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := echoOnce(conn, "bye"); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 16)
			if _, err := conn.Read(buf); err == nil {
				t.Fatal("上游关闭后客户端应读到 EOF / 关闭")
			} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("上游关闭后 3 秒客户端仍未收到结束信号")
			}
			ok, why := waitFor(2*time.Second, func() (bool, string) {
				live := c.liveSessionsForTest(tag)
				return live == 0, fmt.Sprintf("会话=%d", live)
			})
			if !ok {
				t.Fatalf("上游关闭后会话未释放：%s", why)
			}
		})
	}
}
