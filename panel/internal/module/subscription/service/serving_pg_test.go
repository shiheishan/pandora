package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// subNodeFields 从库里取节点协议字段，与 ServingSubscriptions 返回值对照。
func subNodeFields(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) (nodeUID int64, proxyUUID uuid.UUID, periodEnd time.Time) {
	t.Helper()
	if err := e.row(ctx, `SELECT node_uid, proxy_uuid, current_period_end FROM subscriptions WHERE id = ?`, subID).
		Scan(&nodeUID, &proxyUUID, &periodEnd); err != nil {
		t.Fatal(err)
	}
	return nodeUID, proxyUUID, periodEnd
}

func servingByID(list []ServingSubscription) map[uuid.UUID]ServingSubscription {
	m := make(map[uuid.UUID]ServingSubscription, len(list))
	for _, s := range list {
		m[s.ID] = s
	}
	return m
}

func assertServingFields(t *testing.T, got ServingSubscription, wantNodeUID int64, wantProxy uuid.UUID, wantUser, wantVer uuid.UUID, wantEnd time.Time) {
	t.Helper()
	if got.NodeUID != wantNodeUID || got.ProxyUUID != wantProxy || got.UserID != wantUser || got.PlanVersionID != wantVer {
		t.Fatalf("字段不符: got uid=%d proxy=%s user=%s ver=%s; want uid=%d proxy=%s user=%s ver=%s",
			got.NodeUID, got.ProxyUUID, got.UserID, got.PlanVersionID,
			wantNodeUID, wantProxy, wantUser, wantVer)
	}
	if !got.PeriodEnd.Equal(wantEnd) {
		t.Fatalf("PeriodEnd: got %v want %v", got.PeriodEnd, wantEnd)
	}
}

func assertNodeUIDOrder(t *testing.T, list []ServingSubscription) {
	t.Helper()
	for i := 1; i < len(list); i++ {
		if list[i-1].NodeUID >= list[i].NodeUID {
			t.Fatalf("NodeUID 未严格升序: [%d]=%d [%d]=%d", i-1, list[i-1].NodeUID, i, list[i].NodeUID)
		}
	}
}

func TestServingSubscriptionsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()

	planQ, verQuota := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(gib)})
	planNQ, verNoQuota := newPlan(t, e, ctx)

	// 生效、配额有余量
	u1 := newUser(t, e, ctx)
	subActive := provision(t, e, ctx, u1, planQ, verQuota, "生效有余量")

	// 没有配额行
	u2 := newUser(t, e, ctx)
	subNoQuota := provision(t, e, ctx, u2, planNQ, verNoQuota, "无配额")

	// 配额用尽、无流量包
	u3 := newUser(t, e, ctx)
	subQuotaOut := provision(t, e, ctx, u3, planQ, verQuota, "配额用尽")
	setConsumed(t, e, ctx, subQuotaOut.ID, MetricTraffic, PeriodCycle, gib)

	// 配额用尽、流量包有余量
	u4 := newUser(t, e, ctx)
	subPackLeft := provision(t, e, ctx, u4, planQ, verQuota, "包有余")
	setConsumed(t, e, ctx, subPackLeft.ID, MetricTraffic, PeriodCycle, gib)
	e.exec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes, consumed_bytes)
		VALUES (?, ?, 'admin', ?, 100, 10)`, u4, subPackLeft.ID, uuid.New())

	// 配额用尽、流量包也用完
	u5 := newUser(t, e, ctx)
	subPackOut := provision(t, e, ctx, u5, planQ, verQuota, "包用尽")
	setConsumed(t, e, ctx, subPackOut.ID, MetricTraffic, PeriodCycle, gib)
	e.exec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes, consumed_bytes)
		VALUES (?, ?, 'admin', ?, 100, 100)`, u5, subPackOut.ID, uuid.New())

	// expired
	u6 := newUser(t, e, ctx)
	subExpired := provision(t, e, ctx, u6, planQ, verQuota, "已过期状态")
	e.exec(t, ctx, `UPDATE subscriptions SET status = 'expired' WHERE id = ?`, subExpired.ID)

	// active 但周期已结束
	u7 := newUser(t, e, ctx)
	subPastEnd := provision(t, e, ctx, u7, planQ, verQuota, "周期已过")
	e.exec(t, ctx, `UPDATE subscriptions SET current_period_start = now() - interval '2 days',
		current_period_end = now() - interval '1 hour' WHERE id = ?`, subPastEnd.ID)

	// device_limit = 0
	u8 := newUser(t, e, ctx)
	subDevZero := provision(t, e, ctx, u8, planQ, verQuota, "设备不限覆盖")
	e.exec(t, ctx, `UPDATE subscriptions SET device_limit = 0 WHERE id = ?`, subDevZero.ID)

	tracked := []uuid.UUID{
		subActive.ID, subNoQuota.ID, subQuotaOut.ID, subPackLeft.ID, subPackOut.ID,
		subExpired.ID, subPastEnd.ID, subDevZero.ID,
	}
	wantIn := map[uuid.UUID]bool{
		subActive.ID: true, subNoQuota.ID: true, subPackLeft.ID: true, subDevZero.ID: true,
	}

	list, err := e.svc.ServingSubscriptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertNodeUIDOrder(t, list)

	byID := servingByID(list)
	for _, id := range tracked {
		in := wantIn[id]
		if _, ok := byID[id]; ok != in {
			t.Fatalf("订阅 %s 应在名单=%v 实际=%v", id, in, ok)
		}
	}

	// 生效有余量：字段与默认 DeviceLimit
	uid, proxy, end := subNodeFields(t, e, ctx, subActive.ID)
	assertServingFields(t, byID[subActive.ID], uid, proxy, u1, verQuota, end)
	if byID[subActive.ID].DeviceLimit != nil {
		t.Fatal("默认订阅 DeviceLimit 应为 nil")
	}

	uid, proxy, end = subNodeFields(t, e, ctx, subNoQuota.ID)
	assertServingFields(t, byID[subNoQuota.ID], uid, proxy, u2, verNoQuota, end)

	uid, proxy, end = subNodeFields(t, e, ctx, subPackLeft.ID)
	assertServingFields(t, byID[subPackLeft.ID], uid, proxy, u4, verQuota, end)

	uid, proxy, end = subNodeFields(t, e, ctx, subDevZero.ID)
	assertServingFields(t, byID[subDevZero.ID], uid, proxy, u8, verQuota, end)
	if byID[subDevZero.ID].DeviceLimit == nil || *byID[subDevZero.ID].DeviceLimit != 0 {
		t.Fatalf("device_limit=0 时 DeviceLimit 应为 0，得 %v", byID[subDevZero.ID].DeviceLimit)
	}

	// 只对本用例建的 id 做升序抽检：筛出 tracked 且在名单里的，顺序应与全表一致
	var filtered []ServingSubscription
	for _, s := range list {
		for _, id := range tracked {
			if s.ID == id && wantIn[id] {
				filtered = append(filtered, s)
				break
			}
		}
	}
	assertNodeUIDOrder(t, filtered)
}
