package nodefabric

import (
	"crypto/rand"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
)

func recentKeyOf(i int) recentKey {
	var k recentKey
	k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
	return k
}

func TestRecentNonceSetExpiresAndStaysBounded(t *testing.T) {
	clock := newFakeClock()
	r := newRecentSet(4)
	a := recentKeyOf(1)
	if !r.claim(a, clock.Now()) || r.claim(a, clock.Now()) {
		t.Fatal("recent set must claim once")
	}
	clock.Advance(signedNonceRetention - time.Nanosecond)
	if r.claim(a, clock.Now()) {
		t.Fatal("entry released before its retention ended")
	}
	clock.Advance(time.Nanosecond)
	if !r.claim(a, clock.Now()) {
		t.Fatal("expired entry still blocks")
	}
	if len(r.entries) != 1 || r.order.len() != 1 {
		t.Fatalf("expired entries not pruned: %d %d", len(r.entries), r.order.len())
	}
	for i := 2; i <= 10; i++ {
		r.claim(recentKeyOf(i), clock.Now())
		if len(r.entries) > 4 || len(r.entries) != r.order.len() {
			t.Fatalf("over the cap: %d entries, %d queued", len(r.entries), r.order.len())
		}
	}
}

// 触顶挤掉 Valkey 没确认持有的条目（回落到 PG 的）时抬补查界：签名时间戳不晚于它的认领
// 时刻 + 5 分钟的请求要在 PG 补查；挤掉 Valkey 持有的条目不抬界。
func TestRecentNonceEvictionRaisesRecheck(t *testing.T) {
	clock := newFakeClock()
	r := newRecentSet(2)
	stored, fallback := recentKeyOf(1), recentKeyOf(2)
	r.claim(stored, clock.Now())
	r.markStored(stored)
	claimedAt := clock.Now()
	clock.Advance(time.Second)
	r.claim(fallback, clock.Now()) // 没有 markStored：回落条目
	clock.Advance(time.Second)
	r.claim(recentKeyOf(3), clock.Now()) // 挤掉 stored
	if r.recheckNs != 0 {
		t.Fatal("evicting a Valkey-held entry raised the recheck bound")
	}
	r.claim(recentKeyOf(4), clock.Now()) // 挤掉 fallback
	want := claimedAt.Add(time.Second + SignedRequestAcceptanceWindow)
	if !r.needsRecheck(want) || r.needsRecheck(want.Add(time.Second)) {
		t.Fatalf("recheck bound = %s, want %s", time.Unix(0, r.recheckNs).UTC(), want)
	}
	// 被挤掉的那条重放：签名时间戳一定不晚于认领时刻 + 5 分钟
	if !r.needsRecheck(claimedAt.Add(time.Second + SignedRequestAcceptanceWindow)) {
		t.Fatal("replay of the evicted fallback entry escapes the recheck")
	}
}

// 守卫层面的触顶：近期集满额挤掉回落条目后，恢复的 Valkey 没见过它，补查界让重放撞 PG。
func TestNonceEvictedFallbackEntryStillRefusedAfterRecovery(t *testing.T) {
	clock := newFakeClock()
	store := &fakeNonceStore{}
	ledger := &fakeLedger{}
	g := newNonceGuard(store, nil, clock.Now)
	g.recent.max = 3
	store.set(errors.New("valkey down"))
	victimTS := clock.Now()
	if err := claimThrough(g, ledger, 1, victimTS); err != nil {
		t.Fatal(err)
	}
	store.set(nil)
	clock.Advance(nonceStoreCooldown)
	for i := byte(2); i <= 6; i++ { // 挤掉 1
		if err := claimThrough(g, ledger, i, clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := claimThrough(g, ledger, 1, victimTS); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of an evicted fallback nonce = %v", err)
	}
}

func TestKeyRingKeepsOrderAcrossResizes(t *testing.T) {
	var q keyRing
	next, want := 0, 0
	for round := 0; round < 3; round++ {
		for i := 0; i < 5000; i++ {
			q.push(recentKeyOf(next))
			next++
		}
		for q.len() > 100 {
			if q.front() != recentKeyOf(want) {
				t.Fatalf("round %d: front = %v, want %d", round, q.front(), want)
			}
			q.pop()
			want++
		}
	}
	if len(q.buf) > 4*keyRingMinCap {
		t.Fatalf("ring did not shrink: cap %d for %d keys", len(q.buf), q.len())
	}
}

func TestRecentKeySeparatesKindsTenantsAndSubjects(t *testing.T) {
	nonce := make([]byte, 16)
	keys := map[recentKey]string{}
	for _, c := range []struct {
		kind            byte
		tenant, subject string
	}{
		{recentKindNode, "t", "n"}, {recentKindServer, "t", "n"}, {recentKindNode, "t2", "n"},
		{recentKindNode, "t", "n2"}, {recentKindNode, "tn", ""}, {recentKindNode, "", "tn"},
	} {
		k := makeRecentKey(c.kind, c.tenant, c.subject, nonce)
		if prev, ok := keys[k]; ok {
			t.Fatalf("%+v collides with %s", c, prev)
		}
		keys[k] = c.tenant + "/" + c.subject
	}
}

// 近期集的实测占用（-v 看数）。改前 map[string]time.Time 加 []recentNonce 实测：
// 6.7 万条 15.59MB（233 字节一条），20 万条 40.81MB（204 字节一条）。
func TestRecentNonceSetMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("memory measurement")
	}
	for _, n := range []int{67_000, 200_000} {
		tenant := uuid.NewString()
		nodes := make([]string, 1000)
		for i := range nodes {
			nodes[i] = uuid.NewString()
		}
		nonce := make([]byte, 16)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		r := newRecentSet(recentNonceMax)
		now := time.Now()
		for i := 0; i < n; i++ {
			_, _ = rand.Read(nonce)
			r.claim(makeRecentKey(recentKindNode, tenant, nodes[i%len(nodes)], nonce), now)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		used := float64(after.HeapAlloc) - float64(before.HeapAlloc)
		t.Logf("entries=%d heap=%.2fMB bytes/entry=%.0f", r.len(), used/1e6, used/float64(n))
		if per := used / float64(n); per > 150 {
			t.Fatalf("recent set uses %.0f bytes per entry", per)
		}
		runtime.KeepAlive(r)
	}
}
