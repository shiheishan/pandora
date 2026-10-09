package cache

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// 下面的用例由 nodefabric/epoch_watch_test.go 里测监听本身的那几条搬来（断言不变），
// 用节点通道的两类载荷 'd'（下标 0）、'c'（下标 1）。

const testChannel = "aegis_test_epoch"

func newTestWatch(now func() time.Time) *Watch {
	return NewWatch(testChannel, "测试", []string{"d", "c"}, now)
}

// 健康要三样：连着、收到过本进程探针的回声、回声没超时。缺一样戳就是零值，调用方走 PG。
func TestWatchStampNeedsConnectionAndFreshEcho(t *testing.T) {
	clock := newFakeClock()
	w := newTestWatch(clock.Now)
	if w.Stamp().OK() {
		t.Fatal("never-connected watch reported healthy")
	}
	w.Connect()
	if w.Stamp().OK() {
		t.Fatal("watch healthy before the first probe echo proved LISTEN is live")
	}
	w.Observe(w.NextProbe())
	first := w.Stamp()
	if !first.OK() || first.Session() != 1 {
		t.Fatalf("stamp after echo = %+v", first)
	}
	clock.Advance(WatchStaleAfter - time.Millisecond)
	if !w.Stamp().OK() {
		t.Fatal("watch went stale before WatchStaleAfter")
	}
	clock.Advance(time.Millisecond)
	if w.Stamp().OK() {
		t.Fatal("watch still healthy after WatchStaleAfter without an echo")
	}
	w.Observe(w.NextProbe())
	if !w.Stamp().OK() {
		t.Fatal("a fresh echo did not restore health")
	}
	w.Disconnect()
	if w.Stamp().OK() {
		t.Fatal("disconnected watch reported healthy")
	}
	w.Connect()
	w.Observe(w.NextProbe())
	if s := w.Stamp(); s.Session() != 2 || s.Count(0) != 0 || s.Count(1) != 0 {
		t.Fatalf("reconnect must open a new session with fresh counters: %+v", s)
	}
	var nilWatch *Watch
	if nilWatch.Stamp().OK() {
		t.Fatal("nil watch reported healthy")
	}
}

// 认得的载荷只推自己的计数，认不出的载荷每类都推（多算一次，不会少算）。
func TestWatchCountsNotificationKinds(t *testing.T) {
	clock := newFakeClock()
	w := newTestWatch(clock.Now)
	w.Connect()
	w.Observe(w.NextProbe())
	base := w.Stamp()
	w.Observe("d")
	afterD := w.Stamp()
	if afterD.Count(0) != base.Count(0)+1 || afterD.Count(1) != base.Count(1) {
		t.Fatalf("'d' moved the wrong counter: %+v -> %+v", base, afterD)
	}
	w.Observe("c")
	afterC := w.Stamp()
	if afterC.Count(1) != afterD.Count(1)+1 || afterC.Count(0) != afterD.Count(0) {
		t.Fatalf("'c' moved the wrong counter: %+v -> %+v", afterD, afterC)
	}
	w.Observe("something-new")
	afterX := w.Stamp()
	if afterX.Count(1) != afterC.Count(1)+1 || afterX.Count(0) != afterC.Count(0)+1 {
		t.Fatalf("unknown payload must invalidate every kind: %+v -> %+v", afterC, afterX)
	}
	w.Disconnect()
	w.Observe("d")
	w.Connect()
	if s := w.Stamp(); s.OK() {
		t.Fatalf("stamp healthy before the new session's first echo: %+v", s)
	}
}

// 覆盖关系：同一会话、加载时收到的通知不少于请求时；换了会话或请求不健康一律不覆盖。
func TestStampCovers(t *testing.T) {
	req := MakeStamp(3, 5, 7)
	cases := []struct {
		entry      Stamp
		want       Stamp
		dOK, cfgOK bool
	}{
		{MakeStamp(3, 5, 7), req, true, true},
		{MakeStamp(3, 6, 9), req, true, true},
		{MakeStamp(3, 4, 7), req, false, true},
		{MakeStamp(3, 5, 6), req, true, false},
		{MakeStamp(2, 9, 9), req, false, false},
		{Stamp{}, req, false, false},
		{MakeStamp(3, 5, 7), Stamp{}, false, false},
	}
	for i, c := range cases {
		if got := c.entry.Covers(c.want, 0); got != c.dOK {
			t.Errorf("#%d Covers(%+v, %+v, d) = %v", i, c.entry, c.want, got)
		}
		if got := c.entry.Covers(c.want, 1); got != c.cfgOK {
			t.Errorf("#%d Covers(%+v, %+v, c) = %v", i, c.entry, c.want, got)
		}
		if got := c.entry.Covers(c.want, 0, 1); got != (c.dOK && c.cfgOK) {
			t.Errorf("#%d Covers(%+v, %+v, d+c) = %v", i, c.entry, c.want, got)
		}
	}
	if f := MakeStamp(3, 5, 7).Flight("w"); f != "w3.5.7" {
		t.Fatalf("flight tag = %q", f)
	}
}

func TestNewWatchRejectsBadConfig(t *testing.T) {
	for name, build := range map[string]func(){
		"quoted channel": func() { NewWatch(`x"; DROP`, "", []string{"d"}, nil) },
		"upper channel":  func() { NewWatch("Aegis", "", []string{"d"}, nil) },
		"no kinds":       func() { NewWatch("aegis_x", "", nil, nil) },
		"probe kind":     func() { NewWatch("aegis_x", "", []string{"p:x"}, nil) },
		"too many":       func() { NewWatch("aegis_x", "", strings.Split("a,b,c,d,e,f,g,h,i", ","), nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: NewWatch accepted it", name)
				}
			}()
			build()
		}()
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
func TestWatchListenerReconnectsWithNewSession(t *testing.T) {
	w := newTestWatch(nil)
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
	if sql := <-first.listen; sql != "LISTEN "+testChannel {
		t.Fatalf("listener ran %q", sql)
	}
	first.notes <- &pgconn.Notification{Channel: testChannel, Payload: w.NextProbe()}
	first.notes <- &pgconn.Notification{Channel: testChannel, Payload: "d"}
	first.notes <- &pgconn.Notification{Channel: "other", Payload: "c"}
	waitFor(t, func() bool { s := w.Stamp(); return s.OK() && s.Count(0) == 1 })
	if s := w.Stamp(); s.Count(1) != 0 {
		t.Fatalf("notification on another channel was counted: %+v", s)
	}

	close(first.notes) // 连接断了
	waitFor(t, func() bool { return !w.Stamp().OK() })
	<-first.closed

	second := <-conns // WatchRetryDelay 之后重连
	<-second.listen
	second.notes <- &pgconn.Notification{Channel: testChannel, Payload: w.NextProbe()}
	waitFor(t, func() bool { s := w.Stamp(); return s.OK() && s.Session() == 2 && s.Count(0) == 0 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(WatchRetryDelay + 3*time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 新鲜度按本进程探针的发送时刻算：回声一直迟到 4 秒（监听落后）就不健康，哪怕回声每秒都到；
// 别的进程的探针、序号不认识的探针都不计。
func TestWatchFreshnessFollowsProbeSendTime(t *testing.T) {
	clock := newFakeClock()
	w := newTestWatch(clock.Now)
	w.Connect()
	w.Observe(w.NextProbe())
	if !w.Stamp().OK() {
		t.Fatal("prompt echo not healthy")
	}
	// 阈值钉在 3 秒：监听落后 4 秒（下面的模拟）必须判不健康，阈值改回 5 秒这条测试就红
	if WatchStaleAfter > 3*time.Second {
		t.Fatalf("WatchStaleAfter = %s; the revocation bound under a lagging listener must stay <= 3s", WatchStaleAfter)
	}
	var inFlight []string
	for i := 0; i < 20; i++ {
		inFlight = append(inFlight, w.NextProbe())
		clock.Advance(time.Second)
		if len(inFlight) > 3 { // 每秒一条探针，回声在发出 4 秒后到达
			w.Observe(inFlight[0])
			inFlight = inFlight[1:]
		}
		if i >= 3 && w.Stamp().OK() {
			t.Fatalf("listener lagging 4s still healthy at second %d", i+1)
		}
	}
	w.Observe("p:someone-else:" + strconv.FormatUint(w.probeSeq, 10))
	w.Observe("p:" + w.instance + ":999999")
	w.Observe("p:garbage")
	if w.Stamp().OK() {
		t.Fatal("foreign or unknown probes restored health")
	}
	// 监听追上之后立刻恢复
	w.Observe(w.NextProbe())
	if !w.Stamp().OK() {
		t.Fatal("prompt echo after catching up did not restore health")
	}
}

type stalledListenConn struct{ fakeListenConn }

func (c *stalledListenConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	<-ctx.Done() // 连接无声断掉：什么都收不到、也不报错
	return nil, ctx.Err()
}

// 看门狗：监听连着却久久没有本进程探针的回声，主动断开、换新会话重连。
func TestWatchWatchdogReconnectsStalledListener(t *testing.T) {
	w := newTestWatch(nil)
	w.watchdogAfter, w.watchdogEvery = 50*time.Millisecond, 5*time.Millisecond
	acquired := make(chan struct{}, 4)
	acquire := func(context.Context) (listenConn, error) {
		acquired <- struct{}{}
		return &stalledListenConn{fakeListenConn{listen: make(chan string, 1), closed: make(chan struct{})}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runListener(ctx, acquire, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	<-acquired
	select {
	case <-acquired: // 看门狗断开后的重连（隔 WatchRetryDelay）
	case <-time.After(WatchRetryDelay + 3*time.Second):
		t.Fatal("stalled listener was never reconnected")
	}
	cancel()
	<-done
	if w.Stamp().OK() {
		t.Fatal("stalled listener reported healthy")
	}
}

// 读纪元的子查询带 is_called（新建序列的第一次推进只翻 is_called）。
func TestEpochSQLCountsFirstAdvance(t *testing.T) {
	if got := EpochSQL(NodeDeliveryEpoch); got != `(SELECT last_value + is_called::int FROM node_delivery_epoch)` {
		t.Fatalf("EpochSQL = %s", got)
	}
	for _, k := range CacheKinds() {
		if !validChannel(EpochSequence(k)) {
			t.Fatalf("kind %q does not make a plain sequence name", k)
		}
	}
	if !validChannel(CacheEpochChannel) || !validChannel(NodeEpochChannel) {
		t.Fatal("channel names must be plain lowercase identifiers")
	}
}

// Freshness：监听健康时按戳比、能 pinned；不健康时按纪元比、不 pinned；没读纪元的条目不算覆盖。
func TestFreshnessCoversByStampOrEpoch(t *testing.T) {
	watched := Freshness{Stamp: MakeStamp(2, 4, 0)}
	entry := Freshness{Stamp: MakeStamp(2, 4, 0), Epoch: 9}
	if !entry.Covers(watched, 0) || !entry.Pinned(watched, 0) {
		t.Fatal("entry with the same stamp must be served and pinned")
	}
	if newer := (Freshness{Stamp: MakeStamp(2, 5, 0)}); entry.Covers(newer, 0) || entry.Pinned(newer, 0) {
		t.Fatal("a notification after the load must invalidate the entry")
	}
	unwatched := Freshness{Epoch: 9}
	if !entry.Covers(unwatched, 0) || entry.Pinned(unwatched, 0) {
		t.Fatal("unwatched: same epoch serves, but only within the TTL")
	}
	if entry.Covers(Freshness{Epoch: 10}, 0) {
		t.Fatal("unwatched: an advanced epoch must invalidate the entry")
	}
	if (Freshness{Stamp: MakeStamp(2, 4, 0)}).Covers(unwatched, 0) {
		t.Fatal("an entry that never read the epoch must not satisfy an epoch requirement")
	}
	if watched.Flight() == unwatched.Flight() || unwatched.Flight() != "e9" {
		t.Fatalf("flight tags: %q %q", watched.Flight(), unwatched.Flight())
	}
}
