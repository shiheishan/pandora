package nodefabric

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 健康要三样：连着、收到过本进程探针的回声、回声没超时。缺一样戳就是零值，调用方走 PG。
func TestEpochWatchStampNeedsConnectionAndFreshEcho(t *testing.T) {
	clock := newFakeClock()
	w := newEpochWatch(clock.Now)
	if w.stamp().ok() {
		t.Fatal("never-connected watch reported healthy")
	}
	w.connect()
	if w.stamp().ok() {
		t.Fatal("watch healthy before the first probe echo proved LISTEN is live")
	}
	w.observe(w.nextProbe())
	first := w.stamp()
	if !first.ok() || first.session != 1 {
		t.Fatalf("stamp after echo = %+v", first)
	}
	clock.Advance(watchStaleAfter - time.Millisecond)
	if !w.stamp().ok() {
		t.Fatal("watch went stale before watchStaleAfter")
	}
	clock.Advance(time.Millisecond)
	if w.stamp().ok() {
		t.Fatal("watch still healthy after watchStaleAfter without an echo")
	}
	w.observe("p:9")
	if !w.stamp().ok() {
		t.Fatal("a fresh echo did not restore health")
	}
	w.disconnect()
	if w.stamp().ok() {
		t.Fatal("disconnected watch reported healthy")
	}
	w.connect()
	w.observe("p:10")
	if s := w.stamp(); s.session != 2 || s.delivery != 0 || s.config != 0 {
		t.Fatalf("reconnect must open a new session with fresh counters: %+v", s)
	}
}

// 'd' 只推下发计数，'c' 只推配置计数，认不出的载荷两样都推（多算一次，不会少算）。
func TestEpochWatchCountsNotificationKinds(t *testing.T) {
	clock := newFakeClock()
	w := newEpochWatch(clock.Now)
	w.connect()
	w.observe("p:1")
	base := w.stamp()
	w.observe("d")
	afterD := w.stamp()
	if afterD.delivery != base.delivery+1 || afterD.config != base.config {
		t.Fatalf("'d' moved the wrong counter: %+v -> %+v", base, afterD)
	}
	w.observe("c")
	afterC := w.stamp()
	if afterC.config != afterD.config+1 || afterC.delivery != afterD.delivery {
		t.Fatalf("'c' moved the wrong counter: %+v -> %+v", afterD, afterC)
	}
	w.observe("something-new")
	afterX := w.stamp()
	if afterX.config != afterC.config+1 || afterX.delivery != afterC.delivery+1 {
		t.Fatalf("unknown payload must invalidate both: %+v -> %+v", afterC, afterX)
	}
	w.disconnect()
	w.observe("d")
	w.connect()
	if s := w.stamp(); s.ok() {
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

type fakeListenConn struct {
	notes  chan *pgconn.Notification
	listen chan string
	closed chan struct{}
	once   sync.Once
}

func (c *fakeListenConn) Exec(_ context.Context, sql string) error {
	c.listen <- sql
	return nil
}

func (c *fakeListenConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case n, ok := <-c.notes:
		if !ok {
			return nil, errors.New("connection lost")
		}
		return n, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *fakeListenConn) Close() { c.once.Do(func() { close(c.closed) }) }

// 监听循环：LISTEN 之后才算连上；连接断了立刻不健康，换新会话重连。
func TestEpochWatchListenerReconnectsWithNewSession(t *testing.T) {
	w := newEpochWatch(nil)
	conns := make(chan *fakeListenConn, 2)
	acquire := func(context.Context) (listenConn, error) {
		c := &fakeListenConn{notes: make(chan *pgconn.Notification, 4), listen: make(chan string, 1),
			closed: make(chan struct{})}
		conns <- c
		return c, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runListener(ctx, acquire, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	first := <-conns
	if sql := <-first.listen; sql != "LISTEN "+epochWatchChannel {
		t.Fatalf("listener ran %q", sql)
	}
	first.notes <- &pgconn.Notification{Channel: epochWatchChannel, Payload: "p:1"}
	first.notes <- &pgconn.Notification{Channel: epochWatchChannel, Payload: "d"}
	first.notes <- &pgconn.Notification{Channel: "other", Payload: "c"}
	waitFor(t, func() bool { s := w.stamp(); return s.ok() && s.delivery == 1 })
	if s := w.stamp(); s.config != 0 {
		t.Fatalf("notification on another channel was counted: %+v", s)
	}

	close(first.notes) // 连接断了
	waitFor(t, func() bool { return !w.stamp().ok() })
	<-first.closed

	second := <-conns // watchRetryDelay 之后重连
	<-second.listen
	second.notes <- &pgconn.Notification{Channel: epochWatchChannel, Payload: "p:2"}
	waitFor(t, func() bool { s := w.stamp(); return s.ok() && s.session == 2 && s.delivery == 0 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(watchRetryDelay + 3*time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 纪元监听健康时，戳覆盖请求的缓存条目不看 TTL；条目自己的到期时刻照样作数。
func TestTTLCachePinnedEntriesIgnoreTTLButNotHardExpiry(t *testing.T) {
	clock := newFakeClock()
	c := newTTLCache[nodeUserSet](5*time.Second, 8, clock.Now)
	c.expiry = func(set nodeUserSet) time.Time { return set.nextExpiry }
	loads := 0
	hard := clock.Now().Add(time.Minute)
	load := func(context.Context) (nodeUserSet, error) {
		loads++
		return nodeUserSet{version: "v", nextExpiry: hard}, nil
	}
	always := func(nodeUserSet) bool { return true }
	get := func(pinned func(nodeUserSet) bool) {
		t.Helper()
		if _, err := c.get(context.Background(), "k", "", always, pinned, load); err != nil {
			t.Fatal(err)
		}
	}
	get(always)
	clock.Advance(30 * time.Second) // 过了 TTL，没到 hard
	get(always)
	if loads != 1 {
		t.Fatalf("pinned entry reloaded before its hard expiry: loads=%d", loads)
	}
	get(nil) // 不 pinned 时照旧按 TTL 重算
	if loads != 2 {
		t.Fatalf("unpinned entry past TTL was not reloaded: loads=%d", loads)
	}
	clock.Advance(2 * time.Minute) // 过了 hard
	hard = clock.Now().Add(time.Minute)
	get(always)
	if loads != 3 {
		t.Fatalf("pinned entry served past its hard expiry: loads=%d", loads)
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
	w.observe("c")
	if changed, _ := p.poll(context.Background(), q, now, nil); changed {
		t.Fatal("a config-only notification is not a delivery change")
	}
	w.observe("d")
	if changed, _ := p.poll(context.Background(), q, now, nil); !changed {
		t.Fatal("delivery notification not reported")
	}
	w.disconnect()
	w.connect()
	w.observe("p:2")
	if changed, _ := p.poll(context.Background(), q, now, nil); !changed {
		t.Fatal("a new watch session must count as a change")
	}
}
