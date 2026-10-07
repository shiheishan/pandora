package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 夹具
// ---------------------------------------------------------------------------

// streamScript 是假面板对第 i 次连接的回应；超出脚本长度的连接一律 503。
type streamScript []func(w http.ResponseWriter, r *http.Request)

// fakeStreamClock 是事件流用的假时钟：健康口径要求连接活过 40 秒，测试里由
// 假面板在断开前把时钟拨过去，不真等。
type fakeStreamClock struct{ offset atomic.Int64 }

var streamTestBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (c *fakeStreamClock) now() time.Time {
	return streamTestBase.Add(time.Duration(c.offset.Load()))
}

func (c *fakeStreamClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// liveFor 正常建流、发一帧心跳，连接「活」了 d 之后断开（拨假时钟）。
func liveFor(clock *fakeStreamClock, d time.Duration) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		healthyThenDrop(w, r)
		clock.advance(d)
	}
}

// silentFor 回 200 之后一个字节都不发，「活」了 d 之后断开。
func silentFor(clock *fakeStreamClock, d time.Duration) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		acceptThenDrop(w, r)
		clock.advance(d)
	}
}

func refuse(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// healthyThenDrop 正常建流、发一帧心跳，立刻断开——「回一行就断」：反代读超时
// 小于心跳间隔、面板崩溃循环时就是这样。它不能算一次健康连接。
func healthyThenDrop(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": keepalive\n\n")
	w.(http.Flusher).Flush()
}

// acceptThenDrop 回 200 之后一个字节都不发就断开——反代或刚起来又崩掉的
// 面板会这样。它不能算一次健康连接。
func acceptThenDrop(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// recordStreamWaits 按脚本跑 Stream，记下每次重连前要求等待的时长，
// 记满 n 次即结束。等待被替换成立即返回，整个测试不真等。
func recordStreamWaits(t *testing.T, clock *fakeStreamClock, script streamScript, n int) []time.Duration {
	t.Helper()
	var attempt atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(attempt.Add(1)) - 1
		if i < len(script) {
			script[i](w, r)
			return
		}
		refuse(w, r)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	c.streamNow = clock.now
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	var waits []time.Duration
	c.streamWait = func(_ context.Context, d time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return len(waits) < n
	}
	c.Stream(ctx, make(chan StreamEvent, 8), nil)
	if ctx.Err() != nil {
		t.Fatalf("Stream 没在记满 %d 次等待前结束，只记到 %v", n, waits)
	}
	return waits
}

// assertTiers 核对每次等待落在哪一档：档位 b 的等待应在 [b, 1.5b) 之内
// （抖动最多加半档）。
func assertTiers(t *testing.T, waits []time.Duration, tiers ...time.Duration) {
	t.Helper()
	if len(waits) != len(tiers) {
		t.Fatalf("记到 %d 次等待 %v，期望 %d 次", len(waits), waits, len(tiers))
	}
	for i, d := range waits {
		b := tiers[i]
		if d < b || d >= b+b/2 {
			t.Errorf("第 %d 次重连前等了 %s，期望落在 %s 档 [%s, %s)；全部等待 %v",
				i+1, d, b, b, b+b/2, waits)
		}
	}
}

// ---------------------------------------------------------------------------
// 回归测试
// ---------------------------------------------------------------------------

// 连上、正常收到过帧、活过 40 秒之后，退避回到最低档。
//
// 原先退避只翻倍不复位：面板重启过几次之后封顶到 30 秒，此后每次断线都
// 要等 30～45 秒才重连，这段时间里推送全靠轮询兜底。
func TestStreamBackoffResetsAfterHealthyConnection(t *testing.T) {
	clock := &fakeStreamClock{}
	waits := recordStreamWaits(t, clock, streamScript{
		refuse, refuse, refuse, refuse, // 四次连不上：1s → 2s → 4s → 8s
		liveFor(clock, 41*time.Second), // 连上、收到心跳、活过 40 秒后被断开：回到最低档
		refuse,                         // 再断：从最低档重新翻倍
	}, 6)
	assertTiers(t, waits,
		time.Second, 2*time.Second, 4*time.Second, 8*time.Second,
		time.Second, 2*time.Second)
}

// 回了 200 却一帧都没发就断开，不算健康连接，退避照常翻倍。
//
// 否则一个「接了就断」的上游会让节点以最低档反复重连，退避形同虚设。
func TestStreamBackoffKeepsGrowingWhenAcceptedStreamDropsSilently(t *testing.T) {
	waits := recordStreamWaits(t, &fakeStreamClock{}, streamScript{
		acceptThenDrop, acceptThenDrop, acceptThenDrop, acceptThenDrop,
	}, 4)
	assertTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
}

// 「回一行就断」：每次连上都读到一行（面板连上先推全量用户），随即被断开。
//
// 原先读到一行就算健康、退避复位到 1 秒，实测每节点每分钟重连约 48 次（审计
// 故障注入）。现在要活过 40 秒才算健康，退避一路翻倍到 30 秒封顶，稳态重连
// 频率 ≤ 2 次/分钟。
func TestStreamOneLineThenDropBacksOffToTwoPerMinute(t *testing.T) {
	const attempts = 30
	script := make(streamScript, attempts)
	for i := range script {
		script[i] = healthyThenDrop
	}
	waits := recordStreamWaits(t, &fakeStreamClock{}, script, attempts)
	assertTiers(t, waits[:6], time.Second, 2*time.Second, 4*time.Second, 8*time.Second,
		16*time.Second, 30*time.Second)
	var steady time.Duration
	for i, d := range waits[5:] {
		if d < 30*time.Second {
			t.Fatalf("第 %d 次重连只等了 %s，退避被复位了；全部等待 %v", i+6, d, waits)
		}
		steady += d
	}
	perMinute := float64(len(waits[5:])) / steady.Minutes()
	if perMinute > 2 {
		t.Fatalf("稳态重连 %.2f 次/分钟，期望 ≤ 2", perMinute)
	}
	t.Logf("回一行就断：稳态 %.2f 次/分钟（%d 次重连共等 %s）", perMinute, len(waits[5:]), steady.Round(time.Second))
}

// 活过 40 秒但一帧都没读到（面板假死只回了响应头）也不算健康。
func TestStreamBackoffNotResetByLongSilentConnection(t *testing.T) {
	clock := &fakeStreamClock{}
	waits := recordStreamWaits(t, clock, streamScript{
		silentFor(clock, 61*time.Second), silentFor(clock, 61*time.Second), silentFor(clock, 61*time.Second),
	}, 3)
	assertTiers(t, waits, time.Second, 2*time.Second, 4*time.Second)
}

// 第三方面板（Xboard 之类）没有事件流路由，回 404：停掉事件流只走轮询，
// 不再每 30～45 秒敲一次门、打一条日志。
func TestStreamStopsOnNotFound(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	c.streamWait = func(context.Context, time.Duration) bool {
		t.Error("404 之后不该再等待重连")
		return false
	}
	var got []error
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Stream(context.Background(), make(chan StreamEvent, 1), func(err error) { got = append(got, err) })
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("面板回 404 之后 Stream 没有停止")
	}
	if hits.Load() != 1 || len(got) != 1 || !errors.Is(got[0], ErrStreamUnsupported) {
		t.Fatalf("hits=%d errors=%v，期望连一次、收到 ErrStreamUnsupported", hits.Load(), got)
	}
}

// 读期限：面板回了响应头之后不再说话，idle 到点就掐断；每读到一行续期，
// 持续说话的连接不会被误掐。
func TestStreamIdleDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var talk atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if !talk.Load() {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		for i := 0; i < 6; i++ {
			fmt.Fprint(w, ": keepalive\n\n")
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	}))
	defer srv.Close()
	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	c.streamIdle = 150 * time.Millisecond

	start := time.Now()
	_, err := c.streamOnce(context.Background(), make(chan StreamEvent, 1))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("假死的流没有在读期限内断开：err=%v 用时 %s", err, time.Since(start))
	}

	talk.Store(true)
	start = time.Now()
	_, err = c.streamOnce(context.Background(), make(chan StreamEvent, 1))
	if err != nil {
		t.Fatalf("持续发心跳的流被读期限误掐：%v", err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatalf("流提前结束（%s），读期限没有按行续期", time.Since(start))
	}
}
