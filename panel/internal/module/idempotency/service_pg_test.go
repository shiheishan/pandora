package idempotency_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/model"
	"github.com/aegispanel/aegis/internal/module/idempotency/repo"
	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

const testScope = "order_create"

// testFPKey 是测试用的指纹密钥。
var testFPKey = []byte("test-idempotency-fingerprint-key")

func fp(s string) service.Fingerprint {
	return service.NewFingerprint(testFPKey, "POST", "/v1/x", []byte(s))
}

func actorCtx(t *testing.T, d *db.DB, userID uuid.UUID) context.Context {
	return authn.With(testkit.TxCtx(t, d), authn.Principal{UserID: userID})
}

func successResp() service.Response {
	return service.Response{
		Status: 201,
		Body:   []byte(`{"id":"x"}`),
		Header: service.Header{
			ContentType: "application/json; charset=utf-8",
			Location:    "/v1/x/1",
			ETag:        `"e1"`,
		},
	}
}

func scanKeyRow(t *testing.T, ctx context.Context, d *db.DB, id uuid.UUID) (status string, generation int64, replayable bool, responseCode *int) {
	t.Helper()
	err := d.Conn(ctx).Raw(
		"SELECT status, generation, replayable, response_code FROM idempotency_keys WHERE id = ?",
		id,
	).Row().Scan(&status, &generation, &replayable, &responseCode)
	if err != nil {
		t.Fatalf("查行: %v", err)
	}
	return status, generation, replayable, responseCode
}

func dbNow(t *testing.T, ctx context.Context, d *db.DB) time.Time {
	t.Helper()
	var now time.Time
	if err := d.Conn(ctx).Raw("SELECT now()").Row().Scan(&now); err != nil {
		t.Fatalf("SELECT now(): %v", err)
	}
	return now
}

func newSvc(t *testing.T) (*service.Service, *db.DB) {
	d := testkit.DB(t)
	return service.New(repo.New(d), tx.NewManager(d)), d
}

func TestClaimNew(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-new-" + uuid.NewString()

	c, replay, err := svc.Claim(ctx, testScope, key, fp("body1"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if replay != nil {
		t.Fatal("新认领不应有 replay")
	}
	if c == nil {
		t.Fatal("应有 claim")
	}
	if c.Generation != 1 {
		t.Fatalf("Generation = %d，期望 1", c.Generation)
	}
	now := dbNow(t, ctx, d)
	if c.LockedUntil.Before(now.Add(4*time.Minute)) || c.LockedUntil.After(now.Add(6*time.Minute)) {
		t.Fatalf("LockedUntil %v 不在 now+4m..now+6m（now=%v）", c.LockedUntil, now)
	}
	status, _, _, _ := scanKeyRow(t, ctx, d, c.ID)
	if status != model.StatusInFlight {
		t.Fatalf("status = %q，期望 in_flight", status)
	}
}

func TestClaimInFlightConflict(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-busy-" + uuid.NewString()

	_, _, err := svc.Claim(ctx, testScope, key, fp("x"))
	if err != nil {
		t.Fatalf("首次 Claim: %v", err)
	}
	_, _, err = svc.Claim(ctx, testScope, key, fp("x"))
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("期望 conflict，得到 %v", err)
	}
}

func TestClaimLeaseExpiredStillConflict(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-lease-" + uuid.NewString()

	c, _, err := svc.Claim(ctx, testScope, key, fp("x"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := d.Conn(ctx).Exec("UPDATE idempotency_keys SET locked_until = now() - interval '1 minute' WHERE id = ?", c.ID).Error; err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	_, _, err = svc.Claim(ctx, testScope, key, fp("x"))
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("租约过期仍应 conflict，得到 %v", err)
	}
	_, gen, _, _ := scanKeyRow(t, ctx, d, c.ID)
	if gen != 1 {
		t.Fatalf("generation 应仍为 1，得到 %d", gen)
	}
}

func TestClaimFingerprintMismatch(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-fp-" + uuid.NewString()

	t.Run("in_flight", func(t *testing.T) {
		_, _, err := svc.Claim(ctx, testScope, key+"-a", fp("one"))
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		_, _, err = svc.Claim(ctx, testScope, key+"-a", fp("two"))
		if errs.As(err).Code != errs.IdempotencyReuse {
			t.Fatalf("期望 idempotency_key_reuse，得到 %v", err)
		}
	})

	t.Run("succeeded", func(t *testing.T) {
		k := key + "-ok"
		c, _, err := svc.Claim(ctx, testScope, k, fp("done"))
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := svc.Complete(ctx, c, successResp()); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		_, _, err = svc.Claim(ctx, testScope, k, fp("other"))
		if errs.As(err).Code != errs.IdempotencyReuse {
			t.Fatalf("期望 idempotency_key_reuse，得到 %v", err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		k := key + "-fail"
		c, _, err := svc.Claim(ctx, testScope, k, fp("fail-body"))
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := svc.Release(ctx, c, 422); err != nil {
			t.Fatalf("Release: %v", err)
		}
		_, _, err = svc.Claim(ctx, testScope, k, fp("other-fail"))
		if errs.As(err).Code != errs.IdempotencyReuse {
			t.Fatalf("期望 idempotency_key_reuse，得到 %v", err)
		}
	})
}

func TestCompleteAndReplay(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-replay-" + uuid.NewString()
	want := successResp()

	c, _, err := svc.Claim(ctx, testScope, key, fp("r"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := svc.Complete(ctx, c, want); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	_, replay, err := svc.Claim(ctx, testScope, key, fp("r"))
	if err != nil {
		t.Fatalf("再 Claim: %v", err)
	}
	if replay == nil {
		t.Fatal("应有 replay")
	}
	if replay.Status != want.Status || string(replay.Body) != string(want.Body) {
		t.Fatalf("replay 体不一致")
	}
	if replay.Header != want.Header {
		t.Fatalf("replay 头不一致: %+v vs %+v", replay.Header, want.Header)
	}
	status, _, replayable, _ := scanKeyRow(t, ctx, d, c.ID)
	if status != model.StatusSucceeded || !replayable {
		t.Fatalf("行应为 succeeded/replayable，得到 %q replayable=%v", status, replayable)
	}
}

func TestComplete204EmptyBody(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-204-" + uuid.NewString()

	c, _, err := svc.Claim(ctx, testScope, key, fp("n"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := svc.Complete(ctx, c, service.Response{Status: 204}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	_, replay, err := svc.Claim(ctx, testScope, key, fp("n"))
	if err != nil {
		t.Fatalf("再 Claim: %v", err)
	}
	if replay.Status != 204 || len(replay.Body) != 0 {
		t.Fatalf("replay = %d body len %d", replay.Status, len(replay.Body))
	}
}

func TestCompleteUnreplayable(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-unrep-" + uuid.NewString()

	c, _, err := svc.Claim(ctx, testScope, key, fp("u"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := svc.CompleteUnreplayable(ctx, c, 200); err != nil {
		t.Fatalf("CompleteUnreplayable: %v", err)
	}
	_, _, err = svc.Claim(ctx, testScope, key, fp("u"))
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("期望 conflict，得到 %v", err)
	}
}

func TestReleaseTakeoverAndClaimLost(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	key := "k-rel-" + uuid.NewString()
	f := fp("takeover")

	c1, _, err := svc.Claim(ctx, testScope, key, f)
	if err != nil {
		t.Fatalf("Claim c1: %v", err)
	}
	if err := svc.Release(ctx, c1, 422); err != nil {
		t.Fatalf("Release: %v", err)
	}
	c2, _, err := svc.Claim(ctx, testScope, key, f)
	if err != nil {
		t.Fatalf("Claim c2: %v", err)
	}
	if c2.Generation != 2 {
		t.Fatalf("Generation = %d，期望 2", c2.Generation)
	}
	if c2.ID != c1.ID {
		t.Fatal("接管后 ID 应相同")
	}
	if err := svc.Complete(ctx, c1, successResp()); !errors.Is(err, service.ErrClaimLost) {
		t.Fatalf("c1 Complete 应 ErrClaimLost，得到 %v", err)
	}
	if err := svc.Complete(ctx, c2, successResp()); err != nil {
		t.Fatalf("c2 Complete: %v", err)
	}
	if err := svc.Release(ctx, c2, 500); !errors.Is(err, service.ErrClaimLost) {
		t.Fatalf("已完成后再 Release 应 ErrClaimLost，得到 %v", err)
	}
}

func TestCompleteReleaseValidationKeepsInFlight(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)

	cases := []struct {
		name string
		fn   func(*service.Claim) error
	}{
		{"Complete 302", func(c *service.Claim) error {
			return svc.Complete(ctx, c, service.Response{Status: 302, Body: []byte("x")})
		}},
		{"Release 200", func(c *service.Claim) error {
			return svc.Release(ctx, c, 200)
		}},
		{"Location //evil", func(c *service.Claim) error {
			r := successResp()
			r.Header.Location = "//evil"
			return svc.Complete(ctx, c, r)
		}},
		{"Body 超限", func(c *service.Claim) error {
			return svc.Complete(ctx, c, service.Response{
				Status: 200,
				Body:   make([]byte, service.MaxBodyBytes+1),
			})
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "k-val-" + uuid.NewString()
			c, _, err := svc.Claim(ctx, testScope, key, fp(key))
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			if err := tc.fn(c); err == nil {
				t.Fatal("应报错")
			}
			status, _, _, _ := scanKeyRow(t, ctx, d, c.ID)
			if status != model.StatusInFlight {
				t.Fatalf("行应仍为 in_flight，得到 %q", status)
			}
		})
	}
}

func TestActorIsolation(t *testing.T) {
	svc, d := newSvc(t)
	a, b := uuid.New(), uuid.New()
	ctxA := actorCtx(t, d, a)
	ctxB := actorCtx(t, d, b)
	key := "shared-key"

	cA, _, err := svc.Claim(ctxA, testScope, key, fp("actor-a"))
	if err != nil || cA == nil {
		t.Fatalf("A Claim: %v", err)
	}
	cB, _, err := svc.Claim(ctxB, testScope, key, fp("actor-b"))
	if err != nil || cB == nil {
		t.Fatalf("B Claim: %v", err)
	}
	if cA.ID == cB.ID {
		t.Fatal("不同操作者应不同行")
	}
	if err := svc.Complete(ctxA, cA, successResp()); err != nil {
		t.Fatalf("A Complete: %v", err)
	}
	_, _, err = svc.Claim(ctxB, testScope, key, fp("actor-b"))
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("B 处理中应 conflict 而非重放 A，得到 %v", err)
	}
}

func TestClaimAuthAndKeyValidation(t *testing.T) {
	svc, d := newSvc(t)
	ctx := testkit.TxCtx(t, d)

	_, _, err := svc.Claim(ctx, testScope, "ok-key", fp("x"))
	if errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("未登录应 unauthorized，得到 %v", err)
	}
	ctxNil := authn.With(ctx, authn.Principal{UserID: uuid.Nil})
	_, _, err = svc.Claim(ctxNil, testScope, "ok-key", fp("x"))
	if errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("Nil 用户应 unauthorized，得到 %v", err)
	}
	user := uuid.New()
	ctxU := authn.With(ctx, authn.Principal{UserID: user})
	_, _, err = svc.Claim(ctxU, testScope, "bad\x00key", fp("x"))
	if errs.As(err).Code != errs.BadRequest {
		t.Fatalf("非法 key 应 bad_request，得到 %v", err)
	}
}

func TestCompleteBound(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)
	oid := uuid.New()
	key := "k-bound-" + uuid.NewString()

	c, _, err := svc.Claim(ctx, testScope, key, fp("b"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := svc.CompleteBound(ctx, c, successResp(), service.Resource{Type: "order", ID: oid}); err != nil {
		t.Fatalf("CompleteBound: %v", err)
	}
	var rt *string
	var rid *uuid.UUID
	if err := d.Conn(ctx).Raw("SELECT resource_type, resource_id FROM idempotency_keys WHERE id = ?", c.ID).Row().Scan(&rt, &rid); err != nil {
		t.Fatalf("查资源: %v", err)
	}
	if rt == nil || *rt != "order" || rid == nil || *rid != oid {
		t.Fatalf("resource = %v %v", rt, rid)
	}

	c2, _, _ := svc.Claim(ctx, testScope, key+"2", fp("b2"))
	if err := svc.CompleteBound(ctx, c2, successResp(), service.Resource{Type: "Order", ID: oid}); err == nil {
		t.Fatal("大写 Type 应失败")
	}
	c3, _, _ := svc.Claim(ctx, testScope, key+"3", fp("b3"))
	if err := svc.CompleteBound(ctx, c3, successResp(), service.Resource{Type: "order", ID: uuid.Nil}); err == nil {
		t.Fatal("Nil ID 应失败")
	}
}

func TestPurge(t *testing.T) {
	svc, d := newSvc(t)
	user := uuid.New()
	ctx := actorCtx(t, d, user)

	ids := make([]uuid.UUID, 3)
	for i := 0; i < 3; i++ {
		c, _, err := svc.Claim(ctx, testScope, "purge-"+uuid.NewString(), fp("p"))
		if err != nil {
			t.Fatalf("Claim %d: %v", i, err)
		}
		ids[i] = c.ID
	}
	for _, id := range ids[:2] {
		if err := d.Conn(ctx).Exec("UPDATE idempotency_keys SET created_at = '2000-01-01' WHERE id = ?", id).Error; err != nil {
			t.Fatalf("UPDATE created_at: %v", err)
		}
	}
	cutoff := time.Date(2000, 6, 1, 0, 0, 0, 0, time.UTC)
	n1, err := svc.Purge(ctx, cutoff, 1)
	if err != nil || n1 != 1 {
		t.Fatalf("Purge 1: n=%d err=%v", n1, err)
	}
	n2, err := svc.Purge(ctx, cutoff, 1)
	if err != nil || n2 != 1 {
		t.Fatalf("Purge 2: n=%d err=%v", n2, err)
	}
	n3, err := svc.Purge(ctx, cutoff, 1)
	if err != nil || n3 != 0 {
		t.Fatalf("Purge 3: n=%d err=%v", n3, err)
	}
	var left int64
	if err := d.Conn(ctx).Raw("SELECT count(*) FROM idempotency_keys WHERE id = ?", ids[2]).Row().Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("第 3 行应仍在，count=%d", left)
	}
	if _, err := svc.Purge(ctx, time.Time{}, 1); err == nil {
		t.Fatal("before 为零应报错")
	}
	if _, err := svc.Purge(ctx, cutoff, 0); err == nil {
		t.Fatal("batch 为 0 应报错")
	}
}

func TestClaimFromContext(t *testing.T) {
	c := &service.Claim{ID: uuid.New()}
	ctx := service.WithClaim(context.Background(), c)
	got, ok := service.ClaimFrom(ctx)
	if !ok || got != c {
		t.Fatal("应取回同一认领指针")
	}
	_, ok = service.ClaimFrom(context.Background())
	if ok {
		t.Fatal("空 ctx 不应有认领")
	}
}

func TestCompleteInTxRollback(t *testing.T) {
	svc, d := newSvc(t)
	testkit.Truncate(t, d, "idempotency_keys")
	user := uuid.New()
	ctx := authn.With(context.Background(), authn.Principal{UserID: user})
	resp := successResp()

	c, _, err := svc.Claim(ctx, testScope, "tx-roll-"+uuid.NewString(), fp("roll"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	m := tx.NewManager(d)
	err = m.Do(ctx, func(ctx context.Context) error {
		if err := svc.Complete(ctx, c, resp); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("应返回业务失败")
	}
	if c.Completed() {
		t.Fatal("回滚后 Completed 应为 false")
	}
	status, gen, _, _ := scanKeyRow(t, ctx, d, c.ID)
	if status != model.StatusInFlight || gen != 1 {
		t.Fatalf("行应 in_flight gen=1，得到 %q gen=%d", status, gen)
	}

	err = m.Do(ctx, func(ctx context.Context) error {
		return svc.Complete(ctx, c, resp)
	})
	if err != nil {
		t.Fatalf("第二次 Complete: %v", err)
	}
	if !c.Completed() {
		t.Fatal("提交后 Completed 应为 true")
	}
	status, _, replayable, _ := scanKeyRow(t, ctx, d, c.ID)
	if status != model.StatusSucceeded || !replayable {
		t.Fatalf("行应 succeeded，得到 %q", status)
	}
	_, replay, err := svc.Claim(ctx, testScope, c.Key, c.Fingerprint)
	if err != nil || replay == nil {
		t.Fatalf("应能重放: err=%v replay=%v", err, replay)
	}
}

func TestCompleteWithoutTransaction(t *testing.T) {
	svc, d := newSvc(t)
	testkit.Truncate(t, d, "idempotency_keys")
	user := uuid.New()
	ctx := authn.With(context.Background(), authn.Principal{UserID: user})

	c, _, err := svc.Claim(ctx, testScope, "no-tx-"+uuid.NewString(), fp("nt"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := svc.Complete(context.Background(), c, successResp()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !c.Completed() {
		t.Fatal("无事务 Complete 后 Completed 应立即为 true")
	}
}

func TestConcurrentClaim(t *testing.T) {
	svc, d := newSvc(t)
	testkit.Truncate(t, d, "idempotency_keys")
	user := uuid.New()
	ctx := authn.With(context.Background(), authn.Principal{UserID: user})
	key := "conc-" + uuid.NewString()
	finger := fp("same")

	var claims atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _, err := svc.Claim(ctx, testScope, key, finger)
			if err != nil {
				if errs.As(err).Code == errs.Conflict {
					conflicts.Add(1)
					return
				}
				t.Errorf("Claim: %v", err)
				return
			}
			if c != nil {
				claims.Add(1)
			}
		}()
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("恰好 1 个认领，得到 %d", claims.Load())
	}
	if conflicts.Load() != 7 {
		t.Fatalf("其余 7 个应为 conflict，得到 %d", conflicts.Load())
	}
}
