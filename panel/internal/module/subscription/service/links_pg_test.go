package service

// 门户订阅链接列表的 PG 集成测试。

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMyLinks(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	sub2 := provision(t, de.env, ctx, user, planID, versionID, "备用")
	lapse(t, de.env, ctx, sub2.ID, 1)
	sub3 := provision(t, de.env, ctx, user, planID, versionID, "")
	endSub(t, de.env, ctx, sub3.ID)

	links, err := de.svc.MyLinks(userCtx(ctx, user))
	if err != nil {
		t.Fatalf("链接列表: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("应有 2 条（关窗的不列），实际 %d: %+v", len(links), links)
	}
	// 同一事务里凭据 created_at 相同，按 id（uuidv7）倒序：后开的 sub2 在前
	if links[0].SubscriptionID != sub2.ID || links[1].SubscriptionID != sub.ID {
		t.Fatalf("顺序: %v %v", links[0].SubscriptionID, links[1].SubscriptionID)
	}
	for _, l := range links {
		want := testBaseURL + "/" + testPrefix + "/" + de.tokenOf(t, ctx, l.SubscriptionID)
		if l.URL != want {
			t.Fatalf("链接 %q，想要 %q", l.URL, want)
		}
	}
	if !links[0].Expired || links[1].Expired {
		t.Fatalf("Expired: sub2=%v sub=%v", links[0].Expired, links[1].Expired)
	}
}

func TestMyLinksDistinctSources(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	var credID uuid.UUID
	if err := de.row(ctx, `SELECT id FROM subscription_credentials WHERE subscription_id = ? AND status = 'active'`, sub.ID).Scan(&credID); err != nil {
		t.Fatalf("读凭据: %v", err)
	}
	now := time.Now().UTC()
	insert := func(src byte, result string, at time.Time) {
		de.exec(t, ctx, `INSERT INTO subscription_fetch_log (credential_id, subscription_id, ip_hash, ua_family, result, fetched_at)
			VALUES (?, ?, ?, 'clash', ?, ?)`, credID, sub.ID, bytes.Repeat([]byte{src}, 32), result, at)
	}
	insert(1, FetchOK, now)
	insert(2, FetchOK, now)
	insert(1, FetchOK, now)                    // 与第一个同来源
	insert(3, FetchOK, now.Add(-48*time.Hour)) // 窗口外
	insert(4, FetchRateLimited, now)           // 不是成功拉取

	links, err := de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || len(links) != 1 {
		t.Fatalf("链接列表: %v %+v", err, links)
	}
	if links[0].DistinctSources24h != 2 {
		t.Fatalf("近 24 小时不同来源 %d，想要 2", links[0].DistinctSources24h)
	}
}

func TestMyLinksEmpty(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	links, err := de.svc.MyLinks(userCtx(ctx, user))
	if err != nil || links == nil || len(links) != 0 {
		t.Fatalf("没有订阅: %v %#v", err, links)
	}
}
