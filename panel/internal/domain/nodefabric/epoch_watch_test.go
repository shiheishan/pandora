package nodefabric

import (
	"context"
	"testing"
	"time"
)

// 监听本身（健康判定、重连、探针新鲜度、看门狗）的用例随实现搬到了 platform/cache 的
// watch_test.go；这里只留节点链路自己的：两类载荷落到哪个计数、戳的覆盖关系、下发信号循环、
// 名单到期。

// 'd' 只推下发计数，'c' 只推配置计数，认不出的载荷两样都推（多算一次，不会少算）。
func TestEpochWatchCountsNotificationKinds(t *testing.T) {
	clock := newFakeClock()
	w := newEpochWatch(clock.Now)
	w.Connect()
	w.Observe(w.NextProbe())
	base := stampOf(w.Stamp())
	w.Observe("d")
	afterD := stampOf(w.Stamp())
	if afterD.delivery != base.delivery+1 || afterD.config != base.config {
		t.Fatalf("'d' moved the wrong counter: %+v -> %+v", base, afterD)
	}
	w.Observe("c")
	afterC := stampOf(w.Stamp())
	if afterC.config != afterD.config+1 || afterC.delivery != afterD.delivery {
		t.Fatalf("'c' moved the wrong counter: %+v -> %+v", afterD, afterC)
	}
	w.Observe("something-new")
	afterX := stampOf(w.Stamp())
	if afterX.config != afterC.config+1 || afterX.delivery != afterC.delivery+1 {
		t.Fatalf("unknown payload must invalidate both: %+v -> %+v", afterC, afterX)
	}
	w.Disconnect()
	w.Observe("d")
	w.Connect()
	if s := stampOf(w.Stamp()); s.ok() {
		t.Fatalf("stamp healthy before the new session's first echo: %+v", s)
	}
}

// 覆盖关系：同一会话、加载时收到的通知不少于请求时；换了会话或请求不健康一律不覆盖。
func TestWatchStampCovers(t *testing.T) {
	req := watchStamp{session: 3, delivery: 5, config: 7}
	cases := []struct {
		entry      watchStamp
		want       watchStamp
		dOK, cfgOK bool
	}{
		{watchStamp{3, 5, 7}, req, true, true},
		{watchStamp{3, 6, 9}, req, true, true},
		{watchStamp{3, 4, 7}, req, false, true},
		{watchStamp{3, 5, 6}, req, true, false},
		{watchStamp{2, 9, 9}, req, false, false},
		{watchStamp{}, req, false, false},
		{watchStamp{3, 5, 7}, watchStamp{}, false, false},
	}
	for i, c := range cases {
		if got := c.entry.deliveryCovers(c.want); got != c.dOK {
			t.Errorf("#%d deliveryCovers(%+v, %+v) = %v", i, c.entry, c.want, got)
		}
		if got := c.entry.configCovers(c.want); got != c.cfgOK {
			t.Errorf("#%d configCovers(%+v, %+v) = %v", i, c.entry, c.want, got)
		}
	}
}

// 下发信号循环：监听健康时只比 'd' 计数、不读库（Service 没有连接池，读库会 panic）；
// 第一次与换会话都算变化。
func TestEpochPollerFollowsWatchWithoutDatabase(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	w := healthyWatch(t, svc)
	p := &epochPoller{s: svc, tenantID: "t1"}
	q := newStreamPushQueue()
	now := time.Now()
	if changed, _ := p.poll(context.Background(), q, now, nil); !changed {
		t.Fatal("first watched poll must report a change")
	}
	if changed, _ := p.poll(context.Background(), q, now, nil); changed {
		t.Fatal("nothing happened but the poller reported a change")
	}
	w.Observe("c")
	if changed, _ := p.poll(context.Background(), q, now, nil); changed {
		t.Fatal("a config-only notification is not a delivery change")
	}
	w.Observe("d")
	if changed, _ := p.poll(context.Background(), q, now, nil); !changed {
		t.Fatal("delivery notification not reported")
	}
	w.Disconnect()
	w.Connect()
	w.Observe(w.NextProbe())
	if changed, _ := p.poll(context.Background(), q, now, nil); !changed {
		t.Fatal("a new watch session must count as a change")
	}
}

// 名单里最早的到期过了就同步重算：pinned 路径与推送路径（不 pinned、在 staleGrace 窗口里）
// 都不能再回含已到期用户的旧名单（审查 #2，基线上同一缺陷一并修）。
func TestUserSetPastNextExpiryNeverServedStale(t *testing.T) {
	for name, pinned := range map[string]func(nodeUserSet) bool{
		"pinned": func(nodeUserSet) bool { return true },
		"push":   func(nodeUserSet) bool { return false },
	} {
		clock := newFakeClock()
		caches := newNodeCaches(clock.Now)
		expiry := clock.Now().Add(8 * time.Second) // 晚于 TTL（5 秒）：入库时 hard=false
		loads := 0
		load := func(context.Context) (nodeUserSet, error) {
			loads++
			if loads == 1 {
				return nodeUserSet{version: "with-expiring-user", nextExpiry: expiry}, nil
			}
			return nodeUserSet{version: "after-expiry"}, nil
		}
		if got, _ := caches.users.Get(context.Background(), "k", "f", always[nodeUserSet], pinned, load); got.version != "with-expiring-user" {
			t.Fatalf("%s: first load %q", name, got.version)
		}
		clock.Advance(9 * time.Second) // 过了到期 1 秒，仍在 TTL + staleGrace 之内
		if got, _ := caches.users.Get(context.Background(), "k", "f", always[nodeUserSet], pinned, load); got.version != "after-expiry" {
			t.Fatalf("%s: list with an expired user served past nextExpiry: %q", name, got.version)
		}
	}
}
