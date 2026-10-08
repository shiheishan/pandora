package nodesim

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 与 pdnd panel/stream_backoff_test.go 同一套脚本：假面板按第 i 次连接回应，
// 超出脚本的连接一律 503。

type streamScript []func(w http.ResponseWriter, r *http.Request)

func streamRefuse(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// streamHealthyThenDrop 正常建流、发一帧心跳，然后像面板重启那样断开
func streamHealthyThenDrop(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": keepalive\n\n")
	w.(http.Flusher).Flush()
}

// fakeStreamClock 是事件流用的假时钟：健康口径要求连接活过 40 秒，假面板在断开前拨过去。
type fakeStreamClock struct{ offset atomic.Int64 }

func (c *fakeStreamClock) now() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(c.offset.Load()))
}

// streamLiveFor 正常建流、发一帧心跳，连接「活」了 d 之后断开（拨假时钟）。
func streamLiveFor(clock *fakeStreamClock, d time.Duration) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		streamHealthyThenDrop(w, r)
		clock.offset.Add(int64(d))
	}
}

// streamAcceptThenDrop 回 200 之后一个字节都不发就断开，不算健康连接
func streamAcceptThenDrop(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
}

// recordStreamWaits 按脚本跑 streamLoop，记下每次重连前要求等待的时长，记满 n 次即结束。
func recordStreamWaits(t *testing.T, clock *fakeStreamClock, script streamScript, resetOnHealthy bool, n int) []time.Duration {
	t.Helper()
	var attempt atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(attempt.Add(1)) - 1
		if i < len(script) {
			script[i](w, r)
			return
		}
		streamRefuse(w, r)
	}))
	defer srv.Close()

	obs := &observer{rec: ltkit.NewRecorder("nodes", time.Second), fleet: newFleetStats(), maxSamples: 1}
	c := newUniClient(srv.URL, "n1", "vless", "tok", "", time.Second, obs, nil)
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
	c.streamLoop(ctx, make(chan streamEvent, 8), resetOnHealthy, nil)
	if ctx.Err() != nil {
		t.Fatalf("streamLoop 没在记满 %d 次等待前结束，只记到 %v", n, waits)
	}
	return waits
}

// assertStreamTiers 核对每次等待落在哪一档：档位 b 的等待应在 [b, 1.5b) 之内。
func assertStreamTiers(t *testing.T, waits []time.Duration, tiers ...time.Duration) {
	t.Helper()
	if len(waits) != len(tiers) {
		t.Fatalf("记到 %d 次等待 %v，期望 %d 次", len(waits), waits, len(tiers))
	}
	for i, d := range waits {
		if b := tiers[i]; d < b || d >= b+b/2 {
			t.Errorf("第 %d 次重连前等了 %s，期望落在 %s 档；全部等待 %v", i+1, d, b, waits)
		}
	}
}

// current 照 pdnd：健康连接之后退避回到最低档。
func TestStreamBackoffResetsAfterHealthyConnection(t *testing.T) {
	clock := &fakeStreamClock{}
	waits := recordStreamWaits(t, clock, streamScript{
		streamRefuse, streamRefuse, streamRefuse, streamRefuse,
		streamLiveFor(clock, 41*time.Second), streamRefuse,
	}, true, 6)
	assertStreamTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second, time.Second, 2*time.Second)
}

// 回了 200 却一帧都没发就断开，不算健康连接，退避照常翻倍。
func TestStreamBackoffKeepsGrowingWhenAcceptedStreamDropsSilently(t *testing.T) {
	waits := recordStreamWaits(t, &fakeStreamClock{}, streamScript{
		streamAcceptThenDrop, streamAcceptThenDrop, streamAcceptThenDrop, streamAcceptThenDrop,
	}, true, 4)
	assertStreamTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
}

// legacy 冻结为改版前的模拟器：健康连接之后也不复位。
func TestLegacyStreamBackoffNeverResets(t *testing.T) {
	waits := recordStreamWaits(t, &fakeStreamClock{}, streamScript{
		streamRefuse, streamRefuse, streamHealthyThenDrop, streamRefuse,
	}, false, 4)
	assertStreamTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
}

// current 照 pdnd：「回一行就断」不算健康，退避一路翻倍到封顶。
func TestStreamBackoffNotResetByOneLineThenDrop(t *testing.T) {
	waits := recordStreamWaits(t, &fakeStreamClock{}, streamScript{
		streamHealthyThenDrop, streamHealthyThenDrop, streamHealthyThenDrop, streamHealthyThenDrop,
	}, true, 4)
	assertStreamTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
}
