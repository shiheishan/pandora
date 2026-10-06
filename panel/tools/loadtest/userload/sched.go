package userload

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// ---------------------------------------------------------------------------
// 开环调度
// ---------------------------------------------------------------------------
//
// 第 k 拍的时刻是 start + k/rate，与响应快慢无关：面板变慢时请求照样按点发出，
// 在途数随之上涨，排队就显现在延迟里。闭环（等上一个回来再发下一个）会在面板
// 变慢时自动降速，测出来的永远是「面板刚好扛得住」的假象（coordinated omission）。
// 调度协程自己被耽搁时会立即补发落下的拍子，所以长期平均速率严格等于 rate。

// openLoop 按 rate 次/秒调用 tick，直到 ctx 结束；tick 必须立刻返回（真正的请求在 limiter 里起协程）。
func openLoop(ctx context.Context, rate float64, tick func()) {
	if rate <= 0 {
		return
	}
	interval := time.Duration(float64(time.Second) / rate)
	start := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	<-timer.C
	for k := int64(0); ; k++ {
		if wait := time.Until(start.Add(time.Duration(k) * interval)); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		tick()
	}
}

// ---------------------------------------------------------------------------
// 在途上限
// ---------------------------------------------------------------------------

// limiter 是在途请求的上限。满了不排队、直接丢弃并计数：排队会把压测机自己的
// 积压算进面板延迟，而丢弃数非零本身就说明这一档速率超出了压测机或面板的承受，
// 结果不能当真（-strict 会因此失败）。
type limiter struct {
	slots chan struct{}
	wg    sync.WaitGroup
}

func newLimiter(n int) *limiter { return &limiter{slots: make(chan struct{}, n)} }

func (l *limiter) try(fn func()) bool {
	select {
	case l.slots <- struct{}{}:
	default:
		return false
	}
	l.wg.Add(1)
	go func() {
		defer func() {
			<-l.slots
			l.wg.Done()
		}()
		fn()
	}()
	return true
}

func (l *limiter) inflight() int { return len(l.slots) }

// drain 等在途请求收尾，最多等 grace；返回是否全部收完。
func (l *limiter) drain(grace time.Duration) bool {
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

// ---------------------------------------------------------------------------
// 一类流量
// ---------------------------------------------------------------------------

type class struct {
	name    string
	rate    float64
	fire    func(ctx context.Context) bool // 返回 false 表示这一拍没发（例如没有可用会话）
	sent    atomic.Int64
	dropped atomic.Int64 // 在途已满
	skipped atomic.Int64 // fire 返回 false
}

// run 跑这一类的开环调度。请求用 reqCtx（不随调度结束而取消），停止调度后在途请求能正常收尾、照常计量。
func (c *class) run(ctx, reqCtx context.Context, lim *limiter) {
	openLoop(ctx, c.rate, func() {
		ok := lim.try(func() {
			if c.fire(reqCtx) {
				c.sent.Add(1)
			} else {
				c.skipped.Add(1)
			}
		})
		if !ok {
			c.dropped.Add(1)
		}
	})
}

// ---------------------------------------------------------------------------
// 进度
// ---------------------------------------------------------------------------

// codeTally 把错误码分布折成进度行要的几个数。
type codeTally struct{ total, ok, c4xx, c429, c5xx, transport uint64 }

func tallyCodes(codes map[string]uint64) codeTally {
	var t codeTally
	for k, v := range codes {
		t.total += v
		if strings.HasPrefix(k, "transport") {
			t.transport += v
			continue
		}
		n, err := strconv.Atoi(k)
		switch {
		case err != nil:
		case n == 429:
			t.c429 += v
		case n >= 500:
			t.c5xx += v
		case n >= 400:
			t.c4xx += v
		default:
			t.ok += v
		}
	}
	return t
}

// progress 每 every 打一行：这一段时间里各类发了多少、回了什么，以及在途与丢弃。
func progress(ctx context.Context, w io.Writer, every time.Duration, rec *ltkit.Recorder, classes []*class, lim *limiter) {
	if every <= 0 {
		return
	}
	start := time.Now()
	tk := time.NewTicker(every)
	defer tk.Stop()
	var prev codeTally
	prevSent := make([]int64, len(classes))
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
		cur := tallyCodes(rec.Snapshot().Totals.Codes)
		var parts []string
		var dropped int64
		for i, c := range classes {
			s := c.sent.Load()
			parts = append(parts, fmt.Sprintf("%s=%d", c.name, s-prevSent[i]))
			prevSent[i] = s
			dropped += c.dropped.Load()
		}
		fmt.Fprintf(w, "[users] +%s  sent %s  2xx/3xx=%d 4xx=%d 429=%d 5xx=%d transport=%d  inflight=%d dropped_total=%d\n",
			time.Since(start).Round(time.Second), strings.Join(parts, " "),
			cur.ok-prev.ok, cur.c4xx-prev.c4xx, cur.c429-prev.c429, cur.c5xx-prev.c5xx,
			cur.transport-prev.transport, lim.inflight(), dropped)
		prev = cur
	}
}
