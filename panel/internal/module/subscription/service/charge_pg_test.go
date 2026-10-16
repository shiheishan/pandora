package service

// ChargeTraffic 的 PG 集成测试：配额扣到负、不限量、送的量算进余量、滚动空窗、站点时区切日、耗尽通知、并发不死锁。

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

// chargeTZSite 是时区可设的站点设置。
type chargeTZSite struct{ tz string }

func (s chargeTZSite) Site(context.Context) (settingssvc.Site, error) {
	return settingssvc.Site{Name: "测试站", Timezone: s.tz}, nil
}

// chargeSub 开一份订阅，套餐只有一条 traffic.bytes 配额（limit 为 nil 表示不限）。
func chargeSub(t *testing.T, e *env, ctx context.Context, period string, limit *int64) Subscription {
	t.Helper()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, period, limit})
	return provision(t, e, ctx, user, planID, versionID, "")
}

// chargeOK 调 ChargeTraffic，出错即失败。
func chargeOK(t *testing.T, e *env, ctx context.Context, charges ...TrafficCharge) map[int64]uuid.UUID {
	t.Helper()
	subs, err := e.svc.ChargeTraffic(ctx, charges)
	if err != nil {
		t.Fatalf("ChargeTraffic: %v", err)
	}
	return subs
}

// chargeUsage 读一份订阅某一天的按日用量（没有行为 -1）。
func chargeUsage(t *testing.T, e *env, ctx context.Context, subID uuid.UUID, day string) int64 {
	t.Helper()
	var n int64 = -1
	_ = e.row(ctx, `SELECT bytes FROM subscription_usage_daily WHERE subscription_id = ? AND to_char(day, 'YYYY-MM-DD') = ?`, subID, day).Scan(&n)
	return n
}

// chargeUsageTotal 读一份订阅全部按日用量之和。
func chargeUsageTotal(t *testing.T, e *env, ctx context.Context, subID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, `SELECT coalesce(sum(bytes), 0) FROM subscription_usage_daily WHERE subscription_id = ?`, subID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChargeQuotaGoesNegativePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(100))
	subs := chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 150})
	if subs[sub.NodeUID] != sub.ID {
		t.Fatalf("uid → 订阅：%v", subs)
	}
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Consumed != 150 {
		t.Fatalf("超额照记在套餐上，consumed 应为 150，得 %d", q.Consumed)
	}
}

func TestChargeUnlimitedPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	sub := chargeSub(t, e, ctx, PeriodCycle, nil)
	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 70})
	if q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle); q.Consumed != 70 {
		t.Fatalf("不限量行也记用量，得 %d", q.Consumed)
	}
}

// TestChargeWithBonusPG 用真实提交：上限 100 已用完、送 50，扣 30 还剩 20 不通知；再扣 30 用尽通知一次。
func TestChargeWithBonusPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(100))
	setConsumed(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle, 100)
	e.exec(t, ctx, `UPDATE quota_balances SET bonus_value = ? WHERE subscription_id = ? AND metric = ? AND period = ?`, 50, sub.ID, MetricTraffic, PeriodCycle)
	base := e.roster.count(sub.ID)

	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 30})
	q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Consumed != 130 {
		t.Fatalf("consumed 应为 130，得 %d", q.Consumed)
	}
	if n := e.roster.count(sub.ID) - base; n != 0 {
		t.Fatalf("还剩 20 额度，不该通知用尽，得 %d", n)
	}

	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 30})
	q = quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle)
	if q.Consumed != 160 {
		t.Fatalf("consumed 应为 160，得 %d", q.Consumed)
	}
	if n := e.roster.count(sub.ID) - base; n != 1 {
		t.Fatalf("用尽应通知 1 次，得 %d", n)
	}
}

func TestChargeMergesSameUIDPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(1000))
	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 10}, TrafficCharge{UID: sub.NodeUID, Bytes: 15})
	if q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle); q.Consumed != 25 {
		t.Fatalf("同一 uid 合并，得 %d", q.Consumed)
	}
	if n := chargeUsageTotal(t, e, ctx, sub.ID); n != 25 {
		t.Fatalf("按日用量 %d", n)
	}
}

func TestChargeRolloverGapPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	now := time.Now().UTC().Truncate(time.Microsecond)
	gap := chargeSub(t, e, ctx, PeriodDay, i64(1000))
	rollSetQuota(t, e, ctx, gap.ID, PeriodDay, now.Add(-25*time.Hour), now.Add(-time.Hour))
	stale := chargeSub(t, e, ctx, PeriodDay, i64(1000))
	rollSetQuota(t, e, ctx, stale.ID, PeriodDay, now.Add(-50*time.Hour), now.Add(-26*time.Hour))

	subs := chargeOK(t, e, ctx, TrafficCharge{UID: gap.NodeUID, Bytes: 10}, TrafficCharge{UID: stale.NodeUID, Bytes: 10})
	if q := quotaOf(t, e, ctx, gap.ID, MetricTraffic, PeriodDay); q.Consumed != 10 {
		t.Fatalf("滚动空窗那一行照扣，得 %d", q.Consumed)
	}
	if q := quotaOf(t, e, ctx, stale.ID, MetricTraffic, PeriodDay); q.Consumed != 0 {
		t.Fatalf("结束超过一天的旧行不扣，得 %d", q.Consumed)
	}
	if subs[stale.NodeUID] != stale.ID || chargeUsageTotal(t, e, ctx, stale.ID) != 10 {
		t.Fatal("旧行不扣，但仍认得订阅、按日用量照记")
	}
}

func TestChargeUsageSiteTimezonePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(1000))
	sh, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skipf("没有时区数据: %v", err)
	}
	// UTC 20:00 在上海已是第二天；取不早于现在的那个 20:00（晚于开通时刻、仍在本期内）
	base := time.Now().UTC().Truncate(24 * time.Hour).Add(20 * time.Hour)
	if base.Before(time.Now()) {
		base = base.Add(24 * time.Hour)
	}
	e.svc.settings = chargeTZSite{tz: "Asia/Shanghai"}
	e.svc.now = func() time.Time { return base }

	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 5})
	if n := chargeUsage(t, e, ctx, sub.ID, base.In(sh).Format(time.DateOnly)); n != 5 {
		t.Fatalf("上海那一天应有 5，得 %d", n)
	}
	if n := chargeUsage(t, e, ctx, sub.ID, base.Format(time.DateOnly)); n != -1 {
		t.Fatalf("UTC 那一天不该有行，得 %d", n)
	}
}

func TestChargeUnknownAndZeroPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(1000))
	unknown := int64(math.MaxInt64 - 7)
	subs := chargeOK(t, e, ctx, TrafficCharge{UID: unknown, Bytes: 10}, TrafficCharge{UID: sub.NodeUID, Bytes: 0})
	if _, ok := subs[unknown]; ok {
		t.Fatal("不存在的 uid 不该在结果里")
	}
	if subs[sub.NodeUID] != sub.ID {
		t.Fatal("0 字节的 uid 也要给出对应的订阅")
	}
	if q := quotaOf(t, e, ctx, sub.ID, MetricTraffic, PeriodCycle); q.Consumed != 0 {
		t.Fatalf("0 字节不扣，得 %d", q.Consumed)
	}
	if n := chargeUsageTotal(t, e, ctx, sub.ID); n != 0 {
		t.Fatalf("0 字节不记按日用量，得 %d", n)
	}
}

// TestChargeExhaustedNotifiesPG 用真实提交：恰好用完时通知一次，之前已无余量的不再通知，不限量的不通知。
func TestChargeExhaustedNotifiesPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sub := chargeSub(t, e, ctx, PeriodCycle, i64(100))
	free := chargeSub(t, e, ctx, PeriodCycle, nil)
	base, baseFree := e.roster.count(sub.ID), e.roster.count(free.ID)

	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 60}, TrafficCharge{UID: free.NodeUID, Bytes: 1 << 40})
	if n := e.roster.count(sub.ID) - base; n != 0 {
		t.Fatalf("还有余量不该通知，得 %d", n)
	}
	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 40})
	if n := e.roster.count(sub.ID) - base; n != 1 {
		t.Fatalf("恰好用完应通知 1 次，得 %d", n)
	}
	chargeOK(t, e, ctx, TrafficCharge{UID: sub.NodeUID, Bytes: 10})
	if n := e.roster.count(sub.ID) - base; n != 1 {
		t.Fatalf("之前已无余量不该再通知，得 %d", n)
	}
	if n := e.roster.count(free.ID) - baseFree; n != 0 {
		t.Fatalf("不限量的不通知，得 %d", n)
	}
}

// TestChargeConcurrentNoDeadlockPG 用真实提交：同一组订阅的两种到达顺序并发记账，不死锁、不丢量。
func TestChargeConcurrentNoDeadlockPG(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := chargeSub(t, e, ctx, PeriodCycle, i64(100*gib))
	b := chargeSub(t, e, ctx, PeriodCycle, i64(100*gib))

	const workers = 20
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			charges := []TrafficCharge{{UID: a.NodeUID, Bytes: 1}, {UID: b.NodeUID, Bytes: 1}}
			if i%2 == 1 {
				charges[0], charges[1] = charges[1], charges[0]
			}
			if _, err := e.svc.ChargeTraffic(ctx, charges); err != nil {
				errCh <- fmt.Errorf("第 %d 个: %w", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	for _, s := range []Subscription{a, b} {
		if q := quotaOf(t, e, ctx, s.ID, MetricTraffic, PeriodCycle); q.Consumed != workers {
			t.Fatalf("consumed 应为 %d，得 %d", workers, q.Consumed)
		}
		if n := chargeUsageTotal(t, e, ctx, s.ID); n != workers {
			t.Fatalf("按日用量应为 %d，得 %d", workers, n)
		}
	}
}
