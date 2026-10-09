package nodefabric

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// TestEffectiveReleasePG18 的子测试（effective 域的 -run 过滤只列了顶层函数名），用自己的
// 租户、节点与服务器（随机 uuid），不和别的子测试共用 nonce 行。
//
// Valkey 出故障再恢复的全程，PG 是真库、Valkey 是假的（fakeNonceStore）：
// 超时 → 回落（行进 PG）→ 恢复（之后不再写 PG）→ 同一 nonce 先经 Valkey 那条路、
// 重启后再经 PG 补查各重放一次，都被拒；启动读出的补查界同时看节点与服务器两张表。
func testNonceOutageRecoveryPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool) {
	tenant, node := uuid.NewString(), uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,'Nonce Outage')`,
		tenant, "nonce-outage-"+tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO nodes(id,tenant_id,name,status) VALUES($1,$2,'nonce-outage','active')`,
		node, tenant); err != nil {
		t.Fatal(err)
	}
	var server string
	if err := admin.QueryRow(ctx, `INSERT INTO servers (tenant_id, name) VALUES ($1,$2) RETURNING id::text`,
		tenant, "nonce-outage-"+uuid.NewString()).Scan(&server); err != nil {
		t.Fatal(err)
	}
	rows := func(table string, nonce []byte) (n int) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id=$1 AND nonce=$2`,
			tenant, nonce).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	nonceOf := func(label string) ([]byte, []byte) {
		sum := sha256.Sum256([]byte(tenant + label))
		fp := sha256.Sum256([]byte("fp" + label))
		return sum[:16], fp[:]
	}
	// 签名时间戳是整秒
	clock := &fakeClock{now: time.Now().UTC().Truncate(time.Second)}
	store := &fakeNonceStore{}
	start := func() *Service {
		svc := NewService(app, nil)
		svc.SetNonceStore(store, nil)
		svc.nonces.now = clock.Now
		svc.PrimeNonceFallback(ctx, tenant)
		return svc
	}
	claim := func(svc *Service, label string, ts time.Time) (bool, error) {
		nonce, fp := nonceOf(label)
		_, known, err := svc.ClaimSignedRequestNonce(ctx, tenant, node, nonce, fp, ts)
		return known, err
	}

	svc := start()
	if svc.nonces.recent.recheckNs != 0 {
		t.Fatal("empty nonce tables still primed a recheck bound")
	}
	if known, err := claim(svc, "a", clock.Now()); err != nil || known {
		t.Fatalf("healthy claim: known %v err %v", known, err)
	}
	a, _ := nonceOf("a")
	if rows("node_request_nonces", a) != 0 {
		t.Fatal("a Valkey claim wrote PG")
	}

	// Valkey 超时：回落 PG，节点与服务器两条路都落库
	store.set(context.DeadlineExceeded)
	tsB := clock.Now()
	if known, err := claim(svc, "b", tsB); err != nil || !known {
		t.Fatalf("fallback claim: known %v err %v", known, err)
	}
	b, _ := nonceOf("b")
	if rows("node_request_nonces", b) != 1 {
		t.Fatal("fallback claim missing from PG")
	}
	sn, sfp := nonceOf("server-b")
	if err := svc.ClaimServerRequestNonce(ctx, tenant, server, sn, sfp, tsB); err != nil {
		t.Fatalf("server fallback claim: %v", err)
	}
	if rows("server_request_nonces", sn) != 1 {
		t.Fatal("server fallback claim missing from PG")
	}
	for _, label := range []string{"a", "b"} {
		if _, err := claim(svc, label, tsB); !errors.Is(err, errNonceReplayed) {
			t.Fatalf("replay of %s during the outage = %v", label, err)
		}
	}

	// 恢复：冷却到期后只走 Valkey，不再写 PG
	store.set(nil)
	clock.Advance(nonceStoreCooldown)
	for _, label := range []string{"c", "d", "e"} {
		if _, err := claim(svc, label, clock.Now()); err != nil {
			t.Fatalf("claim %s after recovery: %v", label, err)
		}
		if n, _ := nonceOf(label); rows("node_request_nonces", n) != 0 {
			t.Fatalf("claim %s after recovery wrote PG", label)
		}
	}
	// 同一 nonce 经 Valkey 那条路重放（近期集拦下）
	if _, err := claim(svc, "b", tsB); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of b after recovery = %v", err)
	}

	// 重启：近期集空了，Valkey 没见过 b；启动读出的补查界让它在 PG 补查、撞主键
	clock.Advance(time.Minute)
	restarted := start()
	if _, err := claim(restarted, "b", tsB); !errors.Is(err, errNonceReplayed) {
		t.Fatalf("replay of b after restart = %v", err)
	}
	if rows("node_request_nonces", b) != 1 {
		t.Fatal("replayed nonce changed the PG ledger")
	}
	if err := restarted.ClaimServerRequestNonce(ctx, tenant, server, sn, sfp, tsB); !errors.Is(err, ErrServerIdentityInvalid) {
		t.Fatalf("server replay after restart = %v", err)
	}
	// 签名时间戳晚于补查界的新请求只走 Valkey
	fresh := clock.Now()
	if known, err := claim(restarted, "f", fresh); err != nil || known {
		t.Fatalf("fresh claim after restart: known %v err %v", known, err)
	}

	// 服务器那张表里有更晚的未过期行：补查界跟着它走
	future := fresh.Add(2 * time.Minute)
	if _, err := admin.Exec(ctx, `INSERT INTO server_request_nonces
		(tenant_id, server_id, nonce, request_fingerprint, request_ts, expires_at)
		VALUES ($1,$2,$3,$4,$5,$5::timestamptz + interval '11 minutes')`,
		tenant, server, []byte("server-future-01"), sfp, future); err != nil {
		t.Fatal(err)
	}
	third := start()
	if known, err := claim(third, "g", fresh); err != nil || !known {
		t.Fatalf("claim inside the primed server bound: known %v err %v", known, err)
	}
	if g, _ := nonceOf("g"); rows("node_request_nonces", g) != 1 {
		t.Fatal("claim inside the primed bound skipped the PG recheck")
	}
}
