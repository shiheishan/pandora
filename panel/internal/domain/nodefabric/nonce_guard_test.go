package nodefabric

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeNonceStore 代替 Valkey：与 SET NX PX 同语义，可注入错误，也可在 gate 上卡住（模拟慢应答）。
type fakeNonceStore struct {
	mu    sync.Mutex
	keys  map[string]time.Duration
	err   error
	calls int
	gate  chan struct{} // 非 nil 时每次认领先等它关闭或 ctx 到期
}

func (f *fakeNonceStore) ClaimNonce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	f.calls++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	if f.keys == nil {
		f.keys = map[string]time.Duration{}
	}
	if _, ok := f.keys[key]; ok {
		return false, nil
	}
	f.keys[key] = ttl
	return true, nil
}

func (f *fakeNonceStore) set(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeNonceStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeLedger 代替 PG 那张 nonce 表：插入撞主键回重放，记下插入次数。
type fakeLedger struct {
	mu      sync.Mutex
	rows    map[string]bool
	inserts int
}

func (l *fakeLedger) claimFunc(key string) func(context.Context) error {
	return func(context.Context) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.inserts++
		if l.rows == nil {
			l.rows = map[string]bool{}
		}
		if l.rows[key] {
			return errNonceReplayed
		}
		l.rows[key] = true
		return nil
	}
}

func (l *fakeLedger) insertCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inserts
}

// clocksFrom 让守卫的单调钟与墙钟都跟着假时钟走（墙钟不跳变的场景）。
func clocksFrom(c *fakeClock) nonceClock {
	start := c.Now()
	return nonceClock{mono: func() time.Duration { return c.Now().Sub(start) }, wall: c.Now}
}

func nonceFixture(i byte) ([]byte, []byte) {
	nonce := make([]byte, 16)
	nonce[0] = i
	fp := sha256.Sum256(nonce)
	return nonce, fp[:]
}

// claimThrough 用 guard 认领节点 n 的第 i 个 nonce，PG 那一侧落在 ledger。
func claimThrough(g *nonceGuard, ledger *fakeLedger, i byte, ts time.Time) error {
	nonce, _ := nonceFixture(i)
	key := makeRecentKey(recentKindNode, "t", "n", nonce)
	storeKey := nonceKeyPrefix + "t:n:" + string(rune('A'+i))
	return g.claim(context.Background(), key, storeKey, ts, errNonceReplayed, ledger.claimFunc(storeKey))
}

// claimHitsDatabase 断言这次认领去了 PG：Service 没有连接池，碰库就 panic。
func claimHitsDatabase(t *testing.T, svc *Service, nonce, fp []byte, ts time.Time) (hit bool) {
	t.Helper()
	defer func() {
		if recover() != nil {
			hit = true
		}
	}()
	_, _, _ = svc.ClaimSignedRequestNonce(context.Background(), "t", "n", nonce, fp, ts)
	return false
}

// 主路径在 Valkey：认领成功不碰库、不带纪元；同一 nonce 再来一次（Valkey 说已占用，
// 或本进程近期集里已有）一律 401。
func TestNonceClaimUsesValkeyAndRejectsReplays(t *testing.T) {
	store := &fakeNonceStore{}
	svc := NewService(nil, nil)
	svc.SetNonceStore(store, nil)
	nonce, fp := nonceFixture(1)
	_, known, err := svc.ClaimSignedRequestNonce(context.Background(), "t", "n", nonce, fp, time.Now())
	if err != nil || known {
		t.Fatalf("Valkey claim = known %v err %v", known, err)
	}
	for key, ttl := range store.keys {
		if ttl != signedNonceRetention || key != nonceKeyPrefix+"t:n:AQAAAAAAAAAAAAAAAAAAAA" {
			t.Fatalf("Valkey key %q ttl %s", key, ttl)
		}
	}
	if _, _, err := svc.ClaimSignedRequestNonce(context.Background(), "t", "n", nonce, fp, time.Now()); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay through the recent set = %v", err)
	}
	// 另一个进程（近期集里没有）也被 Valkey 拦下
	other := NewService(nil, nil)
	other.SetNonceStore(store, nil)
	if _, _, err := other.ClaimSignedRequestNonce(context.Background(), "t", "n", nonce, fp, time.Now()); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay through Valkey = %v", err)
	}
}

// 修复的回归：Valkey 出错一次之后回落 PG，冷却到期、Valkey 恢复，下一个请求起就不再碰库
// （原先此后约 11 分钟每个请求都在 PG 补认领一次）。
func TestNonceRecoveryStopsDatabaseWrites(t *testing.T) {
	clock := newFakeClock()
	store := &fakeNonceStore{}
	svc := NewService(nil, nil)
	svc.SetNonceStore(store, nil)
	svc.nonces.clock = clocksFrom(clock)
	store.set(context.DeadlineExceeded)
	n1, fp1 := nonceFixture(1)
	if !claimHitsDatabase(t, svc, n1, fp1, clock.Now()) {
		t.Fatal("Valkey failure did not fall back to the database")
	}
	calls := store.callCount()
	n2, fp2 := nonceFixture(2)
	if !claimHitsDatabase(t, svc, n2, fp2, clock.Now()) || store.callCount() != calls {
		t.Fatal("during the cooldown the claim must go straight to the database")
	}
	store.set(nil)
	clock.Advance(nonceStoreCooldown)
	for i := byte(3); i < 50; i++ {
		n, fp := nonceFixture(i)
		if claimHitsDatabase(t, svc, n, fp, clock.Now()) {
			t.Fatalf("claim %d after Valkey recovered still wrote the database", i)
		}
		clock.Advance(10 * time.Second)
	}
	// 回落期间在 PG 认领的 nonce，恢复后换到 Valkey 重放：近期集拦下，不用碰库
	if _, _, err := svc.ClaimSignedRequestNonce(context.Background(), "t", "n", n1, fp1, clock.Now()); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("fallback nonce replayed after recovery = %v", err)
	}
}

// 开工说明要求的全程：超时 → 回落 → 恢复 → 重放仍被拒，同一个 nonce 先后经 Valkey、
// 经 PG 各试一次；另一个进程（重启后，近期集是空的）凭启动时读出的补查界照样拦下。
func TestNonceOutageRecoveryKeepsReplaysOut(t *testing.T) {
	clock := newFakeClock()
	store := &fakeNonceStore{}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, clocksFrom(clock))
	ts := clock.Now

	if err := claimThrough(g, ledger, 1, ts()); err != nil || ledger.insertCount() != 0 {
		t.Fatalf("healthy claim: err %v inserts %d", err, ledger.insertCount())
	}
	// Valkey 超时：回落，nonce 2、3 只记在 PG（与近期集）
	store.set(context.DeadlineExceeded)
	beforeOutage := ts()
	for _, i := range []byte{2, 3} {
		if err := claimThrough(g, ledger, i, ts()); err != nil {
			t.Fatalf("fallback claim %d: %v", i, err)
		}
	}
	if ledger.insertCount() != 2 {
		t.Fatalf("fallback inserts = %d, want 2", ledger.insertCount())
	}
	// 回落中重放：Valkey 认领过的 1、PG 认领过的 2 都被拒
	for _, i := range []byte{1, 2} {
		if err := claimThrough(g, ledger, i, ts()); !errors.Is(err, errNonceReplayed) {
			t.Fatalf("replay of %d during the outage = %v", i, err)
		}
	}
	// 恢复：冷却到期后的探测成功，此后只走 Valkey
	store.set(nil)
	clock.Advance(nonceStoreCooldown)
	inserts := ledger.insertCount()
	for _, i := range []byte{4, 5, 6} {
		if err := claimThrough(g, ledger, i, ts()); err != nil {
			t.Fatalf("claim %d after recovery: %v", i, err)
		}
	}
	if ledger.insertCount() != inserts {
		t.Fatalf("recovered claims wrote PG %d times", ledger.insertCount()-inserts)
	}
	// 恢复后重放 PG 认领过的 2（经 Valkey 那条路）：近期集拦下
	if err := claimThrough(g, ledger, 2, beforeOutage); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of a PG-claimed nonce via Valkey = %v", err)
	}

	// 进程重启：近期集是空的，Valkey 没见过 2。启动时读出 PG 未过期行的最大签名时间戳
	// （PrimeNonceFallback 的口径），签名时间戳不晚于它的请求在 PG 补查。
	clock.Advance(time.Minute)
	restarted := newNonceGuard(store, nil, clocksFrom(clock))
	restarted.recent.raiseRecheck(beforeOutage)
	if err := claimThrough(restarted, ledger, 2, beforeOutage); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of a PG-claimed nonce after restart = %v", err)
	}
	if err := claimThrough(restarted, ledger, 1, beforeOutage); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of a Valkey-claimed nonce after restart = %v", err)
	}
	// 新请求的签名时间戳晚于补查界：只走 Valkey
	inserts = ledger.insertCount()
	if err := claimThrough(restarted, ledger, 7, ts()); err != nil || ledger.insertCount() != inserts {
		t.Fatalf("fresh claim after restart: err %v, PG inserts %d", err, ledger.insertCount()-inserts)
	}
}

// 冷却到期后只放一个请求去探测 Valkey；探测在途时其余请求直接走 PG，不跟着等超时。
func TestNonceProbeLetsOnlyOneRequestWaitOnValkey(t *testing.T) {
	clock := newFakeClock()
	store := &fakeNonceStore{}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, clocksFrom(clock))
	store.set(errors.New("valkey down"))
	if err := claimThrough(g, ledger, 1, clock.Now()); err != nil {
		t.Fatal(err)
	}
	store.set(nil)
	gate := make(chan struct{})
	store.mu.Lock()
	store.gate = gate
	store.mu.Unlock()
	clock.Advance(nonceStoreCooldown)

	probeDone := make(chan error, 1)
	go func() { probeDone <- claimThrough(g, ledger, 2, clock.Now()) }()
	for store.callCount() != 2 { // 等探测卡在 Valkey 上
		time.Sleep(time.Millisecond)
	}
	if err := claimThrough(g, ledger, 3, clock.Now()); err != nil {
		t.Fatal(err)
	}
	if store.callCount() != 2 || ledger.insertCount() != 2 {
		t.Fatalf("while probing: Valkey calls %d, PG inserts %d", store.callCount(), ledger.insertCount())
	}
	close(gate)
	if err := <-probeDone; err != nil {
		t.Fatal(err)
	}
	if err := claimThrough(g, ledger, 4, clock.Now()); err != nil || store.callCount() != 3 || ledger.insertCount() != 2 {
		t.Fatalf("after the probe: err %v, Valkey calls %d, PG inserts %d", err, store.callCount(), ledger.insertCount())
	}
	// 探测失败：重新冷却
	store.set(errors.New("valkey down again"))
	if err := claimThrough(g, ledger, 5, clock.Now()); err != nil {
		t.Fatal(err)
	}
	clock.Advance(nonceStoreCooldown / 2)
	calls := store.callCount()
	if err := claimThrough(g, ledger, 6, clock.Now()); err != nil || store.callCount() != calls {
		t.Fatalf("cooldown after a failed probe: err %v, Valkey calls %d", err, store.callCount()-calls)
	}
}

// 真超时：Valkey 不应答，nonceStoreTimeout 到点按出错回落，不把请求挂住，也不放行。
func TestNonceStoreTimeoutFallsBack(t *testing.T) {
	store := &fakeNonceStore{gate: make(chan struct{})}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, nonceClock{})
	start := time.Now()
	if err := claimThrough(g, ledger, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < nonceStoreTimeout || took > nonceStoreTimeout+2*time.Second {
		t.Fatalf("timed-out claim took %s", took)
	}
	if ledger.insertCount() != 1 {
		t.Fatal("timeout did not fall back to PG")
	}
	if err := claimThrough(g, ledger, 1, time.Now()); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay after a timeout = %v", err)
	}
}

// 调用方取消的请求不判 Valkey 坏，也不回落 PG。
func TestNonceCanceledClaimDoesNotFallBack(t *testing.T) {
	store := &fakeNonceStore{gate: make(chan struct{})}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, nonceClock{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nonce, _ := nonceFixture(1)
	err := g.claim(ctx, makeRecentKey(recentKindNode, "t", "n", nonce), "k", time.Now(), errNonceReplayed, ledger.claimFunc("k"))
	if !errors.Is(err, context.Canceled) || ledger.insertCount() != 0 {
		t.Fatalf("canceled claim: err %v, PG inserts %d", err, ledger.insertCount())
	}
	if !g.acquireStore().use {
		t.Fatal("a canceled request marked Valkey as down")
	}
}

// 同一 nonce 并发认领，Valkey 时好时坏：只有一份能过（go test -race 下跑）。
func TestNonceConcurrentClaimsAcceptOnce(t *testing.T) {
	for round := 0; round < 20; round++ {
		store := &fakeNonceStore{}
		ledger := &fakeLedger{}
		g := newNonceGuard(store, nil, nonceClock{})
		if round%2 == 1 {
			store.set(errors.New("flaky"))
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		accepted := 0
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if i == 16 {
					store.set(nil)
				}
				if claimThrough(g, ledger, 9, time.Now()) == nil {
					mu.Lock()
					accepted++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if accepted != 1 {
			t.Fatalf("round %d: same nonce accepted %d times", round, accepted)
		}
	}
}
