package nodefabric

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeNonceStore struct {
	mu    sync.Mutex
	keys  map[string]time.Duration
	err   error
	calls int
}

func (f *fakeNonceStore) ClaimNonce(_ context.Context, key string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
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

func nonceFixture(i byte) ([]byte, []byte) {
	nonce := make([]byte, 16)
	nonce[0] = i
	fp := sha256.Sum256(nonce)
	return nonce, fp[:]
}

// claimDB 断言这次认领去了 PG：Service 没有连接池，碰库就 panic。
func claimHitsDatabase(t *testing.T, svc *Service, nonce, fp []byte) (hit bool) {
	t.Helper()
	defer func() {
		if recover() != nil {
			hit = true
		}
	}()
	_, _, _ = svc.ClaimSignedRequestNonce(context.Background(), "t", "n", nonce, fp, time.Now())
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

// Valkey 出错：回落 PG（不放行、不全拒），冷却期内不再先等 Valkey；之前在 Valkey 认领过的
// nonce 换到 PG 重放，靠本进程近期集拦下。
func TestNonceClaimFallsBackToDatabaseWithoutReopeningReplays(t *testing.T) {
	store := &fakeNonceStore{}
	svc := NewService(nil, nil)
	svc.SetNonceStore(store, nil)
	old, oldFP := nonceFixture(1)
	if _, _, err := svc.ClaimSignedRequestNonce(context.Background(), "t", "n", old, oldFP, time.Now()); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("valkey down")
	fresh, freshFP := nonceFixture(2)
	if !claimHitsDatabase(t, svc, fresh, freshFP) {
		t.Fatal("Valkey failure did not fall back to the database")
	}
	calls := store.calls
	another, anotherFP := nonceFixture(3)
	if !claimHitsDatabase(t, svc, another, anotherFP) || store.calls != calls {
		t.Fatal("during the cooldown the claim must go straight to the database")
	}
	if _, _, err := svc.ClaimSignedRequestNonce(context.Background(), "t", "n", old, oldFP, time.Now()); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("a Valkey-claimed nonce replayed during the outage = %v", err)
	}
}

// PG 里可能还有未过期的 nonce（回落过）时，Valkey 认领成功也要在 PG 再认领一次。
func TestNonceClaimDoubleChecksDatabaseWhileFallbackRowsLive(t *testing.T) {
	store := &fakeNonceStore{}
	svc := NewService(nil, nil)
	svc.SetNonceStore(store, nil)
	svc.nonces.pgActiveUntil = time.Now().Add(time.Minute)
	nonce, fp := nonceFixture(4)
	if !claimHitsDatabase(t, svc, nonce, fp) {
		t.Fatal("Valkey claim inside the PG-active window skipped the database")
	}
	svc.nonces.pgActiveUntil = time.Time{}
	nonce2, fp2 := nonceFixture(5)
	if claimHitsDatabase(t, svc, nonce2, fp2) {
		t.Fatal("Valkey claim outside the PG-active window still hit the database")
	}
}

func TestRecentNonceSetExpiresAndStaysBounded(t *testing.T) {
	clock := newFakeClock()
	g := newNonceGuard(nil, nil, clock.Now)
	if !g.claimRecent("a") || g.claimRecent("a") {
		t.Fatal("recent set must claim once")
	}
	clock.Advance(signedNonceRetention)
	if !g.claimRecent("a") {
		t.Fatal("expired entry still blocks")
	}
	if len(g.recent) != 1 || len(g.order) != 1 {
		t.Fatalf("expired entries not pruned: %d %d", len(g.recent), len(g.order))
	}
}
