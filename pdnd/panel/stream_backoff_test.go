// [INPUT]: 依赖 stream.go 的 Stream 与 client.go 的 streamWait 测试钩子，依赖 net/http/httptest 按脚本逐次回应的假面板
// [OUTPUT]: 对外提供事件流重连退避的复位测试（健康连接后复位、接了就断不复位）
// [POS]: pdnd/panel 的 SSE 退避守卫，与 stream_test.go 互补：那边用真实时间粗测「有退避」，这里替换等待函数、逐次核对每一档等待
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package panel

import (
	"context"
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

func refuse(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// healthyThenDrop 正常建流、发一帧心跳，然后像面板重启那样断开。
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
func recordStreamWaits(t *testing.T, script streamScript, n int) []time.Duration {
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

// 连上并正常收到过帧之后，退避回到最低档。
//
// 原先退避只翻倍不复位：面板重启过几次之后封顶到 30 秒，此后每次断线都
// 要等 30～45 秒才重连，这段时间里推送全靠轮询兜底。
func TestStreamBackoffResetsAfterHealthyConnection(t *testing.T) {
	waits := recordStreamWaits(t, streamScript{
		refuse, refuse, refuse, refuse, // 四次连不上：1s → 2s → 4s → 8s
		healthyThenDrop, // 连上、收到心跳、被断开：回到最低档
		refuse,          // 再断：从最低档重新翻倍
	}, 6)
	assertTiers(t, waits,
		time.Second, 2*time.Second, 4*time.Second, 8*time.Second,
		time.Second, 2*time.Second)
}

// 回了 200 却一帧都没发就断开，不算健康连接，退避照常翻倍。
//
// 否则一个「接了就断」的上游会让节点以最低档反复重连，退避形同虚设。
func TestStreamBackoffKeepsGrowingWhenAcceptedStreamDropsSilently(t *testing.T) {
	waits := recordStreamWaits(t, streamScript{
		acceptThenDrop, acceptThenDrop, acceptThenDrop, acceptThenDrop,
	}, 4)
	assertTiers(t, waits, time.Second, 2*time.Second, 4*time.Second, 8*time.Second)
}
