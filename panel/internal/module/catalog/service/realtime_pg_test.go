package service

import (
	"context"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type rtFakePoolGuard struct{}

func (rtFakePoolGuard) LockAssignablePools(context.Context, []uuid.UUID) error { return nil }
func (rtFakePoolGuard) MarkUsersChanged(context.Context)                       {}

// expectCreatedAndPublished 认领新建并发布一个套餐的推送。CreatePlan 带发布是一个事务，
// 同一事务里同一行只发第一次登记的那条（去重键 topic + id），所以发布时对版本与套餐行的 UPDATE
// 被同事务里先登记的 INSERT 吸收：公共频道只有 plans、plan_versions、prices 三条 INSERT，门户据此重拉。
func expectCreatedAndPublished(t *testing.T, spy *rtSpy, planID uuid.UUID) {
	t.Helper()
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpInsert, "id": planID.String(),
	})
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTableVersions, "op": realtime.OpInsert,
	})
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePrices, "op": realtime.OpInsert,
	})
}

func TestRealtimeCatalogCreatePlan(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	p := publishedPlan(t, ctx, e, publishedPlanOpts{})

	expectCreatedAndPublished(t, spy, p.ID)
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpInsert, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogUpdatePricesAndPools(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)
	e.svc.UsePoolGuard(rtFakePoolGuard{})

	ctx, _ = seedAdmin(t, ctx, e)
	p := publishedPlan(t, ctx, e, publishedPlanOpts{})

	expectCreatedAndPublished(t, spy, p.ID)

	// 改价触发 plans.changed
	amt := decimal.RequireFromString("19.90")
	updated, err := e.svc.UpdatePrices(ctx, p.ID, PricesUpdate{
		ExpectedRowVersion: p.RowVersion,
		Tiers: []TierChange{{
			BillingInterval: IntervalMonth,
			IntervalCount:   1,
			UnitAmount:      &amt,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpUpdate, "id": p.ID.String(),
	})

	// 换线路触发 plans.changed
	poolID := seedPool(t, ctx, e, "active")
	_, err = e.svc.SetPlanPools(ctx, p.ID, PoolsUpdate{
		ExpectedRowVersion: updated.RowVersion,
		PoolIDs:            []uuid.UUID{poolID},
	})
	if err != nil {
		t.Fatal(err)
	}

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpUpdate, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogArchivePlan(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	p := publishedPlan(t, ctx, e, publishedPlanOpts{})

	expectCreatedAndPublished(t, spy, p.ID)

	_, err := e.svc.ArchivePlan(ctx, p.ID, p.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpUpdate, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogUncommittedDoesNotPublish(t *testing.T) {
	e, ctx := newEnv(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	publishedPlan(t, ctx, e, publishedPlanOpts{})

	// ctx 在回滚事务里：AfterCommit 钩子不会执行，Redis 上不应出现推送。
	spy.expectNone(t, 300*time.Millisecond)
}
