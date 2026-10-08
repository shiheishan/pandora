package nodefabric

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestStreamPushQueueCoalesces(t *testing.T) {
	q := newStreamPushQueue()
	q.addNode("n1")
	q.addNode("n1")
	q.addAllUsers()
	q.addAllUsers()
	select {
	case <-q.wake:
	default:
		t.Fatal("queue did not wake the worker")
	}
	select {
	case <-q.wake:
		t.Fatal("queue woke the worker once per event instead of coalescing")
	default:
	}
	nodes, all := q.take()
	if len(nodes) != 1 || nodes[0] != "n1" || !all {
		t.Fatalf("take = %v %v", nodes, all)
	}
	if nodes, all := q.take(); len(nodes) != 0 || all {
		t.Fatalf("queue kept work after take: %v %v", nodes, all)
	}
}

// 节流与合并：信号连发 2 秒（每 10 毫秒一条），worker 两轮的开始至少隔 minGap，
// 每轮取走积压的全部工作；停发之后积压的最后一批也一定跑掉。
func TestPushRoundsThrottleAndCoalesce(t *testing.T) {
	q := newStreamPushQueue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const gap = 300 * time.Millisecond
	var mu sync.Mutex
	var starts []time.Time
	signals, seen := 0, 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPushRounds(ctx, q, gap, func(nodes []string, allUsers bool) {
			mu.Lock()
			starts = append(starts, time.Now())
			seen += len(nodes)
			mu.Unlock()
		})
	}()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		signals++
		q.addNode("n" + strconv.Itoa(signals%7))
		q.addAllUsers()
	}
	time.Sleep(gap + 200*time.Millisecond)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(starts) < 2 || len(starts) > int(2*time.Second/gap)+3 {
		t.Fatalf("%d signals ran %d rounds, want between 2 and %d", signals, len(starts), int(2*time.Second/gap)+3)
	}
	for i := 1; i < len(starts); i++ {
		if d := starts[i].Sub(starts[i-1]); d < gap-5*time.Millisecond {
			t.Fatalf("rounds %d and %d started %v apart, want >= %v", i-1, i, d, gap)
		}
	}
	if nodes, all := q.take(); len(nodes) != 0 || all {
		t.Fatalf("work left behind after the last round: %v %v", nodes, all)
	}
	if seen == 0 || seen >= signals {
		t.Fatalf("rounds saw %d node entries for %d signals: per-node work must be merged", seen, signals)
	}
}

// 最早到期时刻到了才报一次，报过就清掉；没有到期时刻时从不报。
func TestStreamPushQueueExpiryDue(t *testing.T) {
	q := newStreamPushQueue()
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if q.takeExpiryDue(now) {
		t.Fatal("no expiry recorded, nothing is due")
	}
	q.setNextExpiry(now.Add(3 * time.Second))
	if q.takeExpiryDue(now.Add(2 * time.Second)) {
		t.Fatal("expiry reported before it is due")
	}
	if !q.takeExpiryDue(now.Add(3 * time.Second)) {
		t.Fatal("expiry not reported when due")
	}
	if q.takeExpiryDue(now.Add(4 * time.Second)) {
		t.Fatal("expiry reported twice")
	}
}

// 下发信号循环的三路都在：Valkey 事件、纪元轮询（前进后先等提交落定）、到期定时；
// 没挂 realtime 时照样起 watcher（纪元这一路不依赖 Valkey）。
func TestWatchNodeChangesFollowsDeliveryEpoch(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	watch := pkg.Decl("Service.WatchNodeChanges")
	for _, want := range []string{"s.newEpochPoller(tenantID)", "time.NewTicker(nodeEpochPollInterval)",
		"time.After(nodeEpochSettle)", "poller.poll(ctx, queue, now, log)", "queue.addAllUsers()"} {
		if !strings.Contains(watch, want) {
			t.Fatalf("WatchNodeChanges missing %q", want)
		}
	}
	if reg := pkg.Decl("Service.RegisterStream"); strings.Contains(reg, "s.realtime == nil") {
		t.Fatal("the tenant watcher must start without Valkey: the delivery epoch does not depend on it")
	}
	if !strings.Contains(pkg.Decl("epochPoller.poll"), "p.s.CurrentDeliveryEpoch(") {
		t.Fatal("the poller must read the delivery epoch through CurrentDeliveryEpoch")
	}
	if !strings.Contains(pkg.Decl("Service.runStreamPushQueue"), "runPushRounds(ctx, q, nodeFanoutMinInterval,") {
		t.Fatal("push rounds must be throttled by nodeFanoutMinInterval")
	}
	if nodeEpochPollInterval+nodeEpochSettle+nodeFanoutMinInterval > 2*time.Second {
		t.Fatal("poll + settle + throttle must stay inside the 1–2 second delivery budget")
	}
}
