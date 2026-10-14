package service

// 换链接（门户、后台）的 PG 集成测试。

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// rotateSetup 开一份生效中的订阅（装配分发与线路来源）。
func rotateSetup(t *testing.T) (*deliveryEnv, context.Context, uuid.UUID, Subscription) {
	t.Helper()
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	return de, ctx, user, provision(t, de.env, ctx, user, planID, versionID, "")
}

// activeTokenHash 取一份订阅当前有效凭据的令牌哈希。
func activeTokenHash(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) []byte {
	t.Helper()
	var h []byte
	if err := e.row(ctx, `SELECT token_hash FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, subID).Scan(&h); err != nil {
		t.Fatalf("读有效凭据: %v", err)
	}
	return h
}

// proxyUUIDOf 取订阅的节点身份。
func proxyUUIDOf(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `SELECT proxy_uuid FROM subscriptions WHERE id = ?`, subID).Scan(&id); err != nil {
		t.Fatalf("读 proxy_uuid: %v", err)
	}
	return id
}

// assertRotated 核对一次换链接后的凭据形状：旧的吊销（rotated、无宽限、rotated_count=1），有效的恰好一条且不是旧的。
func assertRotated(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, oldHash []byte) {
	t.Helper()
	var status, reason string
	var graceNull bool
	var rotated int
	if err := e.row(ctx, `SELECT status, revoked_reason, grace_until IS NULL, rotated_count
		FROM subscription_credentials WHERE token_hash = ?`, oldHash).Scan(&status, &reason, &graceNull, &rotated); err != nil {
		t.Fatalf("读旧凭据: %v", err)
	}
	if status != "revoked" || reason != rotateReason || !graceNull || rotated != 1 {
		t.Fatalf("旧凭据 status=%s reason=%s grace_null=%v rotated_count=%d", status, reason, graceNull, rotated)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, subID); n != 1 {
		t.Fatalf("有效凭据 %d 条", n)
	}
	if bytes.Equal(activeTokenHash(t, e, ctx, subID), oldHash) {
		t.Fatal("有效凭据还是旧的")
	}
}

func TestMyRotate(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	old := de.tokenOf(t, ctx, sub.ID)
	oldHash := activeTokenHash(t, de.env, ctx, sub.ID)
	oldProxy := proxyUUIDOf(t, de.env, ctx, sub.ID)

	url, err := de.svc.MyRotate(userCtx(ctx, user), sub.ID)
	if err != nil {
		t.Fatalf("换链接: %v", err)
	}
	fresh := de.tokenOf(t, ctx, sub.ID)
	base := testBaseURL + "/" + testPrefix + "/"
	if !strings.HasPrefix(url, base) || strings.TrimPrefix(url, base) != fresh || fresh == old {
		t.Fatalf("新链接 %q，新令牌 %q，旧令牌 %q", url, fresh, old)
	}
	assertRotated(t, de.env, ctx, sub.ID, oldHash)
	if proxyUUIDOf(t, de.env, ctx, sub.ID) == oldProxy {
		t.Fatal("proxy_uuid 没换")
	}
	if n := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_events
		WHERE subscription_id = ? AND event_type = 'link_rotated' AND actor_kind = 'user' AND actor_id = ?`, sub.ID, user); n != 1 {
		t.Fatalf("link_rotated 事件 %d 条", n)
	}
	if n := countRows(t, de.env, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionLinkRotated, sub.ID); n != 1 {
		t.Fatalf("审计 %d 条", n)
	}

	pctx := pullCtx(ctx, "198.51.100.7", "clash")
	if _, err := de.svc.Pull(pctx, PullRequest{Prefix: testPrefix, Token: old}); !errors.Is(err, ErrPullDenied) {
		t.Fatalf("旧令牌应立即失效: %v", err)
	}
	if _, err := de.svc.Pull(pctx, PullRequest{Prefix: testPrefix, Token: fresh}); err != nil {
		t.Fatalf("新令牌拉取: %v", err)
	}
}

func TestMyRotateExpired(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	before := activeTokenHash(t, de.env, ctx, sub.ID)
	lapse(t, de.env, ctx, sub.ID, 1)
	if _, err := de.svc.MyRotate(userCtx(ctx, user), sub.ID); !errors.Is(err, ErrRotateWhileExpired) {
		t.Fatalf("过期换链接: %v", err)
	}
	if !bytes.Equal(activeTokenHash(t, de.env, ctx, sub.ID), before) {
		t.Fatal("被拒后凭据不该变")
	}
}

func TestMyRotateNotOwner(t *testing.T) {
	de, ctx, _, sub := rotateSetup(t)
	other := newUser(t, de.env, ctx)
	if _, err := de.svc.MyRotate(userCtx(ctx, other), sub.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别人的订阅: %v", err)
	}
}

func TestMyRotateNeedsDelivery(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	before := activeTokenHash(t, e, ctx, sub.ID)
	if _, err := e.svc.MyRotate(userCtx(ctx, user), sub.ID); !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("没装配分发: %v", err)
	}
	if !bytes.Equal(activeTokenHash(t, e, ctx, sub.ID), before) {
		t.Fatal("没装配分发时凭据不该变")
	}
}

func TestAdminRotate(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	admin := newUser(t, de.env, ctx)
	// 后台不看状态：过期的也能换
	lapse(t, de.env, ctx, sub.ID, 1)
	oldHash := activeTokenHash(t, de.env, ctx, sub.ID)
	res, err := de.svc.AdminRotate(adminCtx(ctx, admin), sub.ID, "  用户说链接泄露了  ")
	if err != nil {
		t.Fatalf("后台换链接: %v", err)
	}
	if res.SubscriptionID != sub.ID || res.UserEmail != de.users.emails[user] {
		t.Fatalf("结果 %+v", res)
	}
	assertRotated(t, de.env, ctx, sub.ID, oldHash)
	if n := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_events
		WHERE subscription_id = ? AND event_type = 'link_rotated' AND actor_kind = 'admin' AND actor_id = ?`, sub.ID, admin); n != 1 {
		t.Fatalf("后台 link_rotated 事件 %d 条", n)
	}
	if n := countRows(t, de.env, ctx, `SELECT count(*) FROM audit_events
		WHERE action = ? AND resource_id = ? AND data->>'reason' = ? AND data->>'user_id' = ?`,
		ActionLinkRotatedByAdmin, sub.ID, "用户说链接泄露了", user.String()); n != 1 {
		t.Fatalf("后台审计 %d 条", n)
	}
}

func TestAdminRotateValidation(t *testing.T) {
	de, ctx, user, sub := rotateSetup(t)
	admin := newUser(t, de.env, ctx)
	_, err := de.svc.AdminRotate(adminCtx(ctx, admin), sub.ID, "太短")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
		t.Fatalf("原因太短: %v", err)
	}
	_, err = de.svc.AdminRotate(userCtx(ctx, user), sub.ID, "用户说链接泄露了")
	if !errors.As(err, &e) || e.Code != errs.Forbidden {
		t.Fatalf("门户主体: %v", err)
	}
	if _, err := de.svc.AdminRotate(adminCtx(ctx, admin), uuid.New(), "用户说链接泄露了"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的订阅: %v", err)
	}
}
