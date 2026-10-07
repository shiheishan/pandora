package realtime

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// 并发 dispatch 与订阅者注销：旧实现在注销时 close(ch)，而 dispatch 复制目标后
// 解锁再发送，两者撞上就 send on closed channel，consume 没有 recover，整个
// 网关进程崩溃。这里用 ≥2000 次「订阅 → 收发 → 注销」与持续 dispatch 交错，
// 在 -race 下断言零 panic、零数据竞争。
func TestDispatchRacesUnsubscribeWithoutPanic(t *testing.T) {
	h := newTestHub(t, "")
	const (
		channel     = "rt:race-tenant:public"
		subscribers = 4000
		dispatchers = 4
	)

	var panics atomic.Int64
	var dispatched atomic.Int64
	stop := make(chan struct{})
	var dwg sync.WaitGroup
	for d := 0; d < dispatchers; d++ {
		dwg.Add(1)
		go func() {
			defer dwg.Done()
			ev := Event{Topic: "race.tick"}
			for {
				select {
				case <-stop:
					return
				default:
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							panics.Add(1)
						}
					}()
					h.dispatch(channel, ev)
				}()
				dispatched.Add(1)
			}
		}()
	}

	var swg sync.WaitGroup
	for i := 0; i < subscribers; i++ {
		swg.Add(1)
		go func(i int) {
			defer swg.Done()
			ch, unsub := h.Subscribe([]string{channel, fmt.Sprintf("rt:race-tenant:user:%d", i)})
			// 读一两条再走，模拟 SSE 连接正在收事件时断开
			select {
			case <-ch:
			default:
			}
			unsub()
			// 重复注销同样安全
			unsub()
		}(i)
	}
	swg.Wait()
	close(stop)
	dwg.Wait()

	if n := panics.Load(); n != 0 {
		t.Fatalf("dispatch 与注销并发时 panic %d 次", n)
	}
	if got := h.Count(); got != 0 {
		t.Fatalf("全部注销后连接数应为 0，实际 %d", got)
	}
	h.mu.RLock()
	left := len(h.byChannel)
	h.mu.RUnlock()
	if left != 0 {
		t.Fatalf("全部注销后频道索引应为空，实际 %d 个", left)
	}
	t.Logf("订阅/注销 %d 次，并发 dispatch %d 次，panic 0 次", subscribers, dispatched.Load())
}
