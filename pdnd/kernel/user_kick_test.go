package kernel

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 删用户即断线（用户定的「到期立即停」）：删掉的用户的长连接 1 秒内断开，
// 其他用户不受影响。
func TestDelUsersKicksLiveConnections(t *testing.T) {
	for _, p := range lifecycleProtos() {
		t.Run(p.name, func(t *testing.T) {
			echo := startLifecycleEcho(t, false)
			victim, bystander := p.user(0), p.user(1)
			c, port, tag := startLifecycleCore(t, p, []core.User{victim, bystander})
			victimConns := make([]net.Conn, 0, 3)
			for i := 0; i < 3; i++ {
				conn, err := p.dial(port, victim, echo.addr())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := echoOnce(conn, "victim"); err != nil {
					t.Fatal(err)
				}
				victimConns = append(victimConns, conn)
			}
			keep, err := p.dial(port, bystander, echo.addr())
			if err != nil {
				t.Fatal(err)
			}
			defer keep.Close()
			if err := echoOnce(keep, "bystander"); err != nil {
				t.Fatal(err)
			}

			if err := c.DelUsers(tag, []string{victim.UUID}); err != nil {
				t.Fatal(err)
			}
			for i, conn := range victimConns {
				_ = conn.SetReadDeadline(time.Now().Add(time.Second))
				buf := make([]byte, 8)
				_, err := conn.Read(buf)
				if err == nil {
					t.Fatalf("被删用户的第 %d 条连接仍可读", i)
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					t.Fatalf("被删用户的第 %d 条连接 1 秒内没有断开", i)
				}
			}
			if err := echoOnce(keep, "still-here"); err != nil {
				t.Fatalf("其他用户受到影响：%v", err)
			}
			// 被删用户重连会被拒（不在名单里了）。
			if conn, err := p.dial(port, victim, echo.addr()); err == nil {
				// 认证失败的连接服务端读到超时才关（防探测），这里只等 300ms 看有没有回显。
				_ = conn.SetDeadline(time.Now().Add(300 * time.Millisecond))
				_, _ = conn.Write([]byte("again"))
				buf := make([]byte, 5)
				// trojan 认证失败交给回落页，读到的是回落内容而不是回显。
				if n, _ := conn.Read(buf); n > 0 && string(buf[:n]) == "again" {
					t.Fatal("被删用户重连居然拿到了回显")
				}
				_ = conn.Close()
			}
			if live := c.liveSessionsForTest(tag); live != 1 {
				t.Fatalf("删人后在途会话=%d，期望只剩 1 条", live)
			}
		})
	}
}

// 规模缩小版：上千条连接分属几百个用户，一次删 50 人，删除调用本身在毫秒级完成
// （锁内只摘表，关连接在锁外），被删的连接全部断开，其余连接照常可用。
func TestDelUsersAtScaleIsCheap(t *testing.T) {
	p := lifecycleProtos()[0] // vless
	const users, perUser, remove = 250, 4, 50
	n := users * perUser
	if testing.Short() || raceEnabled {
		n = 400
	}
	echo := startLifecycleEcho(t, false)
	all := make([]core.User, users)
	for i := range all {
		all[i] = p.user(i)
	}
	c, port, tag := startLifecycleCore(t, p, all)
	conns := make([]net.Conn, n)
	owner := make([]int, n)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	sem := make(chan struct{}, 64)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			u := i % users
			conn, err := p.dial(port, all[u], echo.addr())
			if err == nil {
				err = echoOnce(conn, "x")
			}
			if err != nil {
				errs <- fmt.Errorf("conn %d: %w", i, err)
				return
			}
			conns[i], owner[i] = conn, u
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	defer func() {
		for _, conn := range conns {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()
	ids := make([]string, remove)
	for i := 0; i < remove; i++ {
		ids[i] = all[i].UUID
	}
	start := time.Now()
	if err := c.DelUsers(tag, ids); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("%d 条连接中删 %d 人：DelUsers 耗时 %s", n, remove, took)
	if took > 500*time.Millisecond {
		t.Fatalf("DelUsers 耗时 %s，不应出现可见尖峰", took)
	}
	wantLive := 0
	for i := range conns {
		if owner[i] >= remove {
			wantLive++
		}
	}
	ok, why := waitFor(time.Second, func() (bool, string) {
		live := c.liveSessionsForTest(tag)
		return live == wantLive, fmt.Sprintf("会话=%d 期望 %d", live, wantLive)
	})
	if !ok {
		t.Fatal(why)
	}
	// 抽查一条没被删的连接仍然可用。
	for i := range conns {
		if owner[i] >= remove {
			if err := echoOnce(conns[i], "alive"); err != nil {
				t.Fatalf("未删用户的连接受影响：%v", err)
			}
			break
		}
	}
}

// 认证与登记之间被删：登记时发现 epoch 之后被撤销过，直接拒绝。
func TestUserSessionsRejectsRevokedDuringHandshake(t *testing.T) {
	var s userSessions
	user := core.User{ID: 7, UUID: "u"}
	epoch := s.epoch()
	s.revoke([]int64{user.ID})
	if sess := s.open(user, epoch, nil); sess != nil {
		t.Fatal("认证期间被撤销的用户不该登记成功")
	}
	// 撤销之后重新加回、新连接（新 epoch）照常登记。
	if sess := s.open(user, s.epoch()); sess == nil {
		t.Fatal("撤销之后的新连接应能登记")
	} else {
		sess.close()
	}
	if s.liveCount() != 0 {
		t.Fatalf("live=%d", s.liveCount())
	}
}
