package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	"github.com/aegispanel/nodeagent/perfgate/internal/stats"
)

// connrateFreshDialer 为 true 时每次迭代新建 Dialer（真握手）。
func connrateFreshDialer(proto string) bool {
	return profile.IsQUIC(proto) || proto == profile.AnyTLS
}

func runConnrate(args []string) int {
	fs := flag.NewFlagSet("connrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var concurrency int
	var duration time.Duration
	var maxRate int
	var common commonFlags
	common.register(fs)
	fs.IntVar(&concurrency, "concurrency", 32, "并发 worker 数")
	fs.DurationVar(&duration, "duration", 60*time.Second, "压测时长")
	fs.IntVar(&maxRate, "max-rate", 0, "全体每秒成功上限，0=不限")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := common.load(); err != nil {
		fmt.Fprintf(os.Stderr, "connrate: %v\n", err)
		return 2
	}
	if common.echo == "" {
		fmt.Fprintf(os.Stderr, "connrate: --echo 必填\n")
		return 2
	}
	if common.node == "" {
		fmt.Fprintf(os.Stderr, "connrate: --node 必填\n")
		return 2
	}
	if common.proto == "" || common.proto == "direct" {
		fmt.Fprintf(os.Stderr, "connrate: --proto 必填\n")
		return 2
	}
	if concurrency < 1 {
		fmt.Fprintf(os.Stderr, "connrate: --concurrency 必须 ≥ 1\n")
		return 2
	}

	users := pickUsers(common.prof, common.clients)
	fresh := connrateFreshDialer(common.proto)

	dialers := make([]Dialer, 0, common.clients)
	if !fresh {
		dialers = make([]Dialer, common.clients)
		for k := 0; k < common.clients; k++ {
			d, err := NewDialer(common.prof, common.proto, common.node, users[k])
			if err != nil {
				fmt.Fprintf(os.Stderr, "connrate: 创建 Dialer: %v\n", err)
				return 1
			}
			dialers[k] = d
		}
		defer func() {
			for _, d := range dialers {
				_ = d.Close()
			}
		}()
	}

	ctx, cancel := signalContext(nil, duration)
	defer cancel()

	t0Ms := nowMs()
	var okCount atomic.Int64
	var failCount atomic.Int64
	connectRec := stats.NewRecorder()
	var errMu sync.Mutex
	errCounts := make(map[string]int)

	var rateCh chan struct{}
	var rateStop chan struct{}
	if maxRate > 0 {
		rateCh = make(chan struct{}, maxRate*2)
		rateStop = make(chan struct{})
		interval := time.Second / time.Duration(maxRate)
		if interval <= 0 {
			interval = time.Nanosecond
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-rateStop:
					return
				case <-ticker.C:
					select {
					case rateCh <- struct{}{}:
					default:
					}
				}
			}
		}()
		defer close(rateStop)
	}

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			var d Dialer
			if !fresh {
				d = dialers[workerID%len(dialers)]
			}
			for {
				if ctx.Err() != nil {
					return
				}
				if maxRate > 0 {
					select {
					case <-ctx.Done():
						return
					case <-rateCh:
					}
				}
				if fresh {
					user := users[workerID%len(users)]
					nd, err := NewDialer(common.prof, common.proto, common.node, user)
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						failCount.Add(1)
						recordConnrateErr(&errMu, errCounts, err.Error())
						continue
					}
					d = nd
				}
				ms, err := echoOnce(ctx, d, common.echo, 10*time.Second)
				if fresh && d != nil {
					_ = d.Close()
					d = nil
				}
				if err != nil {
					if ctx.Err() != nil {
						return // 到时长或收到信号时被打断的那一次不算失败
					}
					failCount.Add(1)
					recordConnrateErr(&errMu, errCounts, err.Error())
					continue
				}
				okCount.Add(1)
				connectRec.Add(ms)
			}
		}(w)
	}
	wg.Wait()
	t1Ms := nowMs()

	ok := okCount.Load()
	fail := failCount.Load()
	secs := float64(t1Ms-t0Ms) / 1000.0
	ratePerS := 0.0
	if secs > 0 {
		ratePerS = float64(ok) / secs
	}

	errMu.Lock()
	errsOut := topErrors(errCounts, 5)
	errMu.Unlock()

	emit(map[string]any{
		"cmd":        "connrate",
		"name":       common.name,
		"proto":      common.proto,
		"ok":         ok,
		"fail":       fail,
		"secs":       secs,
		"rate_per_s": ratePerS,
		"connect_ms": connectRec.Summary(),
		"errors":     errsOut,
		"t0_unix_ms": t0Ms,
		"t1_unix_ms": t1Ms,
	})
	return 0
}

func recordConnrateErr(mu *sync.Mutex, m map[string]int, msg string) {
	key := truncateErr(msg, 80)
	mu.Lock()
	m[key]++
	mu.Unlock()
}

// topErrors 取出现次数最多的前 limit 种错误（键截到 80 字符）。
func topErrors(m map[string]int, limit int) map[string]int {
	if len(m) == 0 || limit <= 0 {
		return map[string]int{}
	}
	type pair struct {
		k string
		v int
	}
	pairs := make([]pair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, pair{k: truncateErr(k, 80), v: v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	out := make(map[string]int)
	for i := 0; i < len(pairs) && i < limit; i++ {
		out[pairs[i].k] = pairs[i].v
	}
	return out
}
