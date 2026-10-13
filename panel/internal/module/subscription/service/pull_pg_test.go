package service

// 订阅拉取的 PG 集成测试。走到 live 或过期提示的用例会调渲染（render 包），p4-render 合入前会 panic。

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// setupPull 准备一份可拉的订阅与标准 pull 上下文。
func setupPull(t *testing.T) (*deliveryEnv, context.Context, uuid.UUID, Subscription, string) {
	t.Helper()
	de := newDeliveryEnv(t, true)
	ctx := pullCtx(de.txCtx(), "198.51.100.7", "clash-verge/v2.0")
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	token := de.tokenOf(t, ctx, sub.ID)
	return de, ctx, user, sub, token
}

func pull(de *deliveryEnv, ctx context.Context, token, prefix string) (PullResult, error) {
	return de.svc.Pull(ctx, PullRequest{Prefix: prefix, Token: token})
}

func TestPullOK(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	res, err := pull(de, ctx, token, testPrefix)
	if err != nil {
		t.Fatalf("第一次拉取: %v", err)
	}
	if res.Expired || res.UpdateIntervalHours != 12 || res.ContentDisposition == "" {
		t.Fatalf("响应头字段: expired=%v interval=%d disposition=%q", res.Expired, res.UpdateIntervalHours, res.ContentDisposition)
	}
	if res.Userinfo.Total != 10*gib || res.Userinfo.Expire != sub.PeriodEnd.Unix() {
		t.Fatalf("Userinfo 首次: %+v periodEnd=%d", res.Userinfo, sub.PeriodEnd.Unix())
	}

	setConsumed(t, de.env, ctx, sub.ID, MetricTraffic, PeriodCycle, 1*gib)
	de.exec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes)
		VALUES (?, ?, 'admin', gen_random_uuid(), ?)`, sub.UserID, sub.ID, 2*gib)

	res2, err := pull(de, ctx, token, testPrefix)
	if err != nil {
		t.Fatalf("第二次拉取: %v", err)
	}
	if res2.Userinfo.Total != 12*gib || res2.Userinfo.Download != 1*gib {
		t.Fatalf("Userinfo 含包与用量: %+v", res2.Userinfo)
	}

	var fetchCount int
	if err := de.env.row(ctx, `SELECT fetch_count FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, sub.ID).
		Scan(&fetchCount); err != nil {
		t.Fatalf("读 fetch_count: %v", err)
	}
	if fetchCount != 2 {
		t.Fatalf("fetch_count=%d want 2", fetchCount)
	}
	var lastFetched interface{}
	if err := de.env.row(ctx, `SELECT last_fetched_at FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, sub.ID).
		Scan(&lastFetched); err != nil || lastFetched == nil {
		t.Fatalf("last_fetched_at 应非空: %v %v", lastFetched, err)
	}
	if de.fetchLogs(t, ctx, &sub.ID, FetchOK) != 2 {
		t.Fatalf("ok 日志条数")
	}
	de.nodes.mu.Lock()
	qs := de.nodes.queries
	de.nodes.mu.Unlock()
	if len(qs) < 2 {
		t.Fatalf("节点查询次数 %d", len(qs))
	}
	q := qs[len(qs)-1]
	if q.SubscriptionID != sub.ID || q.UserID != sub.UserID || q.PlanVersionID != sub.PlanVersionID || q.UserGroupID != nil {
		t.Fatalf("节点查询条件: %+v sub=%+v", q, sub)
	}
}

func TestPullYAMLExtension(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	_, err := pull(de, ctx, token+".yaml", testPrefix)
	if err != nil {
		t.Fatalf("带 .yaml 扩展名: %v", err)
	}
}

func TestPullWrongPrefix(t *testing.T) {
	de, ctx, _, _, token := setupPull(t)
	_, err := pull(de, ctx, token, "ffffffffffff")
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("前缀错: %v", err)
	}
	if de.fetchLogs(t, ctx, nil, FetchNotFound) != 1 {
		t.Fatalf("第一次 not_found 日志")
	}
	_, err = pull(de, ctx, token, "ffffffffffff")
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("第二次前缀错: %v", err)
	}
	if de.fetchLogs(t, ctx, nil, FetchNotFound) != 1 {
		t.Fatalf("采样后 not_found 仍应为 1")
	}
}

func TestPullInvalidToken(t *testing.T) {
	de, ctx, _, _, _ := setupPull(t)
	_, err := pull(de, ctx, "short", testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("令牌太短: %v", err)
	}
	_, err = pull(de, ctx, randHex(32), testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("令牌不存在: %v", err)
	}
}

func TestPullDeliveryUnavailable(t *testing.T) {
	de := newDeliveryEnv(t, false)
	ctx := pullCtx(de.txCtx(), "198.51.100.7", "clash-verge/v2.0")
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	token := de.tokenOf(t, ctx, sub.ID)
	_, err := pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("无线路来源: %v", err)
	}
	if n := countRows(t, de.env, ctx, `SELECT count(*) FROM subscription_fetch_log WHERE subscription_id = ?`, sub.ID); n != 0 {
		t.Fatalf("不应有该订阅的拉取日志: %d", n)
	}
}

func TestPullExpiredRevivable(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_end = now() - interval '1 day',
		current_period_start = now() - interval '31 days' WHERE id = ?`, sub.ID)
	res, err := pull(de, ctx, token, testPrefix)
	if err != nil {
		t.Fatalf("过期可救回: %v", err)
	}
	if !res.Expired || res.UpdateIntervalHours != 1 {
		t.Fatalf("过期提示: expired=%v interval=%d", res.Expired, res.UpdateIntervalHours)
	}
	wantURL := testBaseURL + "/#/checkout?renew=" + sub.ID.String()
	if res.WebPageURL != wantURL {
		t.Fatalf("WebPageURL=%q want %q", res.WebPageURL, wantURL)
	}
	if de.fetchLogs(t, ctx, &sub.ID, FetchExpired) != 1 {
		t.Fatalf("expired 日志")
	}
}

func TestPullActivePastPeriodEnd(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 hour' WHERE id = ?`, sub.ID)
	res, err := pull(de, ctx, token, testPrefix)
	if err != nil {
		t.Fatalf("active 已过到期: %v", err)
	}
	if !res.Expired {
		t.Fatalf("应走过期提示")
	}
}

func TestPullRenewalClosed(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', renewal_closed_at = now() WHERE id = ?`, sub.ID)
	_, err := pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("已关窗: %v", err)
	}
}

func TestPullRevokedGrace(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	if _, err := pull(de, ctx, token, testPrefix); err != nil {
		t.Fatalf("吊销前应先成功: %v", err)
	}
	de.exec(t, ctx, `UPDATE subscription_credentials SET status = 'revoked', revoked_at = now(), revoked_reason = 'rotated' WHERE subscription_id = ?`, sub.ID)
	_, err := pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("吊销无宽限: %v", err)
	}
	de.exec(t, ctx, `UPDATE subscription_credentials SET grace_until = now() + interval '1 hour' WHERE subscription_id = ?`, sub.ID)
	if _, err := pull(de, ctx, token, testPrefix); err != nil {
		t.Fatalf("宽限内: %v", err)
	}
	de.exec(t, ctx, `UPDATE subscription_credentials SET grace_until = now() - interval '1 minute' WHERE subscription_id = ?`, sub.ID)
	_, err = pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("宽限外: %v", err)
	}
}

func TestPullOwnerSuspended(t *testing.T) {
	de, ctx, user, sub, token := setupPull(t)
	de.owners.set(user, "suspended")
	_, err := pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("主人停用: %v", err)
	}
	de.exec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 day' WHERE id = ?`, sub.ID)
	_, err = pull(de, ctx, token, testPrefix)
	if !errors.Is(err, ErrPullDenied) {
		t.Fatalf("过期+主人停用仍应 denied 非过期提示: %v", err)
	}
}

func TestPullRateLimited(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.settings.setLimit(2)
	for i := 0; i < 2; i++ {
		if _, err := pull(de, ctx, token, testPrefix); err != nil {
			t.Fatalf("限流前第 %d 次: %v", i+1, err)
		}
	}
	_, err := pull(de, ctx, token, testPrefix)
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Code != errs.RateLimited || ee.RetryAfterSeconds <= 0 {
		t.Fatalf("第三次应 429: %v", err)
	}
	if de.fetchLogs(t, ctx, &sub.ID, FetchRateLimited) != 1 {
		t.Fatalf("rate_limited 日志")
	}
	var fetchCount int
	if err := de.env.row(ctx, `SELECT fetch_count FROM subscription_credentials WHERE subscription_id = ?`, sub.ID).Scan(&fetchCount); err != nil {
		t.Fatal(err)
	}
	if fetchCount != 2 {
		t.Fatalf("fetch_count=%d want 2", fetchCount)
	}
}

func TestPullNodesError(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	de.nodes.err = errors.New("boom")
	_, err := pull(de, ctx, token, testPrefix)
	if errors.Is(err, ErrPullDenied) {
		t.Fatalf("线路错误不应伪装 denied: %v", err)
	}
	if err == nil {
		t.Fatal("应有错误")
	}
	if de.fetchLogs(t, ctx, &sub.ID, FetchError) != 1 {
		t.Fatalf("error 日志")
	}
}

func TestPullLogNoPlaintext(t *testing.T) {
	de, ctx, _, sub, token := setupPull(t)
	if _, err := pull(de, ctx, token, testPrefix); err != nil {
		t.Fatalf("拉取: %v", err)
	}
	var ipHash, uaHash, ipEnc []byte
	var uaFamily, format *string
	err := de.env.row(ctx, `SELECT ip_hash, ua_hash, ip_enc, ua_enc, ua_family, format FROM subscription_fetch_log
		WHERE subscription_id = ? AND result = 'ok' ORDER BY fetched_at DESC LIMIT 1`, sub.ID).
		Scan(&ipHash, &uaHash, &ipEnc, new([]byte), &uaFamily, &format)
	if err != nil {
		t.Fatalf("读日志行: %v", err)
	}
	if len(ipHash) != 32 || len(uaHash) != 32 {
		t.Fatalf("哈希长度 ip=%d ua=%d", len(ipHash), len(uaHash))
	}
	if len(ipEnc) == 0 || bytes.Contains(ipEnc, []byte("198.51.100.7")) {
		t.Fatalf("ip_enc 不应含明文 IP")
	}
	if uaFamily == nil || strings.TrimSpace(*uaFamily) == "" {
		t.Fatalf("ua_family 应非空")
	}
}
