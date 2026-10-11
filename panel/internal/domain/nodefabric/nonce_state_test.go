package nodefabric

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// jumpClock 的单调钟只往前走，墙钟可以单独跳（运维改时间、NTP 步进）。
type jumpClock struct {
	mu      sync.Mutex
	base    time.Time
	mono    time.Duration
	wallOff time.Duration
}

func newJumpClock() *jumpClock {
	return &jumpClock{base: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)}
}

func (c *jumpClock) clock() nonceClock {
	return nonceClock{
		mono: func() time.Duration { c.mu.Lock(); defer c.mu.Unlock(); return c.mono },
		wall: func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.base.Add(c.mono + c.wallOff) },
	}
}

func (c *jumpClock) advance(d time.Duration)  { c.mu.Lock(); c.mono += d; c.mu.Unlock() }
func (c *jumpClock) jumpWall(d time.Duration) { c.mu.Lock(); c.wallOff += d; c.mu.Unlock() }

// 对抗审查 w12nonce #1、#6：墙钟先前跳 10 分钟、触发一次清理、再跳回，近期集不能提前
// 忘掉条目。回落时只记在 PG 的 nonce 经 Valkey 重放、Valkey 持有的 nonce 在 Valkey
// 又挂了时经 PG 重放，都必须被拒。
func TestNonceWallClockJumpDoesNotReopenReplays(t *testing.T) {
	jc := newJumpClock()
	clock := jc.clock()
	store := &fakeNonceStore{}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, clock)

	stored, storedTS := byte(1), clock.wall()
	if err := claimThrough(g, ledger, stored, storedTS); err != nil {
		t.Fatal(err)
	}
	store.set(errors.New("valkey down"))
	fallback, fallbackTS := byte(2), clock.wall()
	if err := claimThrough(g, ledger, fallback, fallbackTS); err != nil {
		t.Fatal(err)
	}
	store.set(nil)
	jc.advance(nonceStoreCooldown)
	if err := claimThrough(g, ledger, 3, clock.wall()); err != nil { // 探测成功，回到 healthy
		t.Fatal(err)
	}

	jc.advance(2 * time.Minute)
	jc.jumpWall(10 * time.Minute)
	if err := claimThrough(g, ledger, 4, clock.wall()); err != nil { // 跳变期间的请求触发清理
		t.Fatal(err)
	}
	jc.jumpWall(-10 * time.Minute)

	// 签名时间戳仍在窗口内（才过了约 2 分钟）
	if err := claimThrough(g, ledger, fallback, fallbackTS); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("fallback-only nonce replayed via Valkey after a wall-clock jump = %v", err)
	}
	store.set(errors.New("valkey down again"))
	if err := claimThrough(g, ledger, stored, storedTS); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("Valkey-held nonce replayed via PG after a wall-clock jump = %v", err)
	}
}

// 回退 M2：探测被调用方取消后要复位，下一个请求接着探测（否则卡在 probing，永远走 PG）。
func TestNonceCanceledProbeHandsTheProbeOn(t *testing.T) {
	clock := newFakeClock()
	g := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	g.storeFailed(g.acquireStore(), errors.New("down"))
	clock.Advance(nonceStoreCooldown)
	probe := g.acquireStore()
	if !probe.use || !probe.probe {
		t.Fatalf("after the cooldown: %+v", probe)
	}
	if other := g.acquireStore(); other.use {
		t.Fatal("a second request probed while the first probe was in flight")
	}
	g.storeAbandoned(probe)
	if next := g.acquireStore(); !next.use || !next.probe {
		t.Fatalf("after a canceled probe the next request must probe: %+v", next)
	}
}

// 回退 M4：非探测请求的成功（冷却开始前发出、冷却中才回来）不能把状态拉回 healthy。
func TestNonceLateSuccessDoesNotEndCooldown(t *testing.T) {
	clock := newFakeClock()
	g := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	slow, failing := g.acquireStore(), g.acquireStore()
	g.storeFailed(failing, errors.New("down"))
	g.storeSucceeded(slow)
	if t2 := g.acquireStore(); t2.use {
		t.Fatalf("a late non-probe success ended the cooldown: %+v", t2)
	}
}

// 回退 M7：上一轮发出、探测已恢复之后才回来的失败（代号过期）不能把状态打回 down。
func TestNonceStaleFailureAfterRecoveryIsIgnored(t *testing.T) {
	clock := newFakeClock()
	g := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	slow, failing := g.acquireStore(), g.acquireStore()
	g.storeFailed(failing, errors.New("down"))
	clock.Advance(nonceStoreCooldown)
	g.storeSucceeded(g.acquireStore()) // 探测成功：healthy，代号 +1
	g.storeFailed(slow, errors.New("late timeout"))
	if next := g.acquireStore(); !next.use || next.probe {
		t.Fatalf("a stale failure knocked the recovered store down: %+v", next)
	}
	// 当前代号的失败照样生效
	g.storeFailed(g.acquireStore(), errors.New("down again"))
	if next := g.acquireStore(); next.use {
		t.Fatal("a current-generation failure did not start a cooldown")
	}
}

// 回退 M10：启动读失败时补查界是「启动时刻 + 5 分钟」；读到行时是最大签名时间戳 + 1 秒；
// 两张表都空时不补查。
func TestNoncePrimeSetsTheRecheckBound(t *testing.T) {
	clock := newFakeClock()
	start := clock.Now()

	failed := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	failed.applyPrime(nil, errors.New("database unavailable"))
	bound := start.Add(SignedRequestAcceptanceWindow)
	if !failed.recent.needsRecheck(bound) || failed.recent.needsRecheck(bound.Add(time.Nanosecond)) {
		t.Fatalf("bound after a failed read = %s, want %s", time.Unix(0, failed.recent.recheckNs).UTC(), bound)
	}

	latest := start.Add(-3 * time.Minute)
	primed := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	primed.applyPrime(&latest, nil)
	if !primed.recent.needsRecheck(latest.Add(time.Second)) || primed.recent.needsRecheck(latest.Add(time.Second+time.Nanosecond)) {
		t.Fatalf("bound after a read = %s, want %s", time.Unix(0, primed.recent.recheckNs).UTC(), latest.Add(time.Second))
	}

	empty := newNonceGuard(&fakeNonceStore{}, nil, clocksFrom(clock))
	empty.applyPrime(nil, nil)
	if empty.recent.recheckNs != 0 {
		t.Fatal("empty nonce tables primed a recheck bound")
	}
}

// 第三轮（复审信息 1）：条目单调钟到期之后，墙钟往回跳 6 分钟以上、旧签名时间戳重新
// 进窗口，Valkey 键也已按 TTL 过期——近期集不能已经忘掉它。墙钟恢复前进、签名时间戳
// 出窗之后，条目照常删掉。
func TestNonceWallClockStepBackAfterExpiryStillRefusesReplay(t *testing.T) {
	jc := newJumpClock()
	clock := jc.clock()
	store := &fakeNonceStore{}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, clock)

	victim, victimTS := byte(1), clock.wall()
	if err := claimThrough(g, ledger, victim, victimTS); err != nil {
		t.Fatal(err)
	}
	jc.advance(signedNonceRetention + time.Minute)
	jc.jumpWall(-(signedNonceRetention + time.Minute - 4*time.Minute)) // 墙钟回到认领后 4 分钟
	store.mu.Lock()
	store.keys = nil // Valkey 的键已按 TTL 过期
	store.mu.Unlock()
	if err := claimThrough(g, ledger, 2, clock.wall()); err != nil { // 触发清理
		t.Fatal(err)
	}
	if err := claimThrough(g, ledger, victim, victimTS); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay after the wall clock stepped back past the retention = %v", err)
	}

	// 墙钟照常前进到签名时间戳出窗（加余量）之后，下一次清理把它删掉
	jc.advance(2 * time.Minute)
	if err := claimThrough(g, ledger, 3, clock.wall()); err != nil {
		t.Fatal(err)
	}
	nonce, _ := nonceFixture(victim)
	g.recent.mu.Lock()
	_, kept := g.recent.entries[makeRecentKey(recentKindNode, "t", "n", nonce)]
	g.recent.mu.Unlock()
	if kept {
		t.Fatal("entry outlived both its monotonic expiry and the signed-timestamp window")
	}
}

// 删条目要两个条件同时成立：单调钟到期，且按墙钟当前时刻签名时间戳已出窗（5 分钟加余量）。
func TestRecentNonceForgetsOnlyOnceTimestampLeftWindow(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	a := recentKeyOf(1)
	cases := []struct {
		name string
		wall time.Time
		kept bool
	}{
		{"wall clock in step", base.Add(signedNonceRetention), false},
		{"wall clock stepped back", base.Add(SignedRequestAcceptanceWindow), true},
		{"timestamp just inside margin", base.Add(SignedRequestAcceptanceWindow + recentWallMargin), true},
		{"timestamp past margin", base.Add(SignedRequestAcceptanceWindow + recentWallMargin + time.Nanosecond), false},
	}
	for _, c := range cases {
		r := newRecentSet(100)
		r.claim(a, 0, base, base)
		r.claim(recentKeyOf(2), signedNonceRetention, c.wall, c.wall) // 单调钟已到期
		if _, ok := r.entries[a]; ok != c.kept {
			t.Fatalf("%s: kept = %v, want %v", c.name, ok, c.kept)
		}
	}
	// 单调钟没到期时墙钟再往前也不删
	r := newRecentSet(100)
	r.claim(a, 0, base, base)
	r.claim(recentKeyOf(2), signedNonceRetention-time.Nanosecond, base.Add(time.Hour), base.Add(time.Hour))
	if _, ok := r.entries[a]; !ok {
		t.Fatal("entry dropped before its monotonic expiry")
	}
}

// 墙钟回跳期间条目多留，上限照样生效：挤掉的回落条目照旧抬补查界。
func TestRecentNonceCapHoldsWhileWallClockIsBehind(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	behind := base.Add(time.Minute) // 墙钟落后：没有条目能删
	r := newRecentSet(3)
	stored, fallback := recentKeyOf(1), recentKeyOf(2)
	r.claim(stored, 0, base, base)
	r.markStored(stored)
	r.claim(fallback, 0, base, base.Add(2*time.Second))
	for i := 3; i <= 8; i++ {
		r.claim(recentKeyOf(i), signedNonceRetention+time.Duration(i), behind, behind)
		if r.len() > 3 {
			t.Fatalf("cap broken while the wall clock is behind: %d entries", r.len())
		}
	}
	if !r.needsRecheck(base.Add(2 * time.Second)) {
		t.Fatal("evicting a fallback entry while the wall clock is behind did not raise the recheck bound")
	}
}
