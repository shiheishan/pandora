package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/shopspring/decimal"
)

func TestRealtimeCatalogWizardCreate(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	p := publishedPlan(t, ctx, e, publishedPlanOpts{})

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpInsert, "id": p.ID.String(),
	})
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTableVersions, "op": realtime.OpInsert,
	})
	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePrices, "op": realtime.OpInsert,
	})
	spy.expect(t, realtime.ChannelAdmin(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpInsert, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogArchivePlan(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	p := publishedPlan(t, ctx, e, publishedPlanOpts{})

	_, err := e.svc.ArchivePlan(ctx, p.ID, p.RowVersion)
	if err != nil {
		t.Fatal(err)
	}

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpUpdate, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogStock(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	total := 5
	p := publishedPlan(t, ctx, e, publishedPlanOpts{StockTotal: &total})

	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := e.svc.ReserveStock(ctx, p.ID, 1)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("期望预留成功")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePlans, "op": realtime.OpUpdate, "id": p.ID.String(),
	})
}

func TestRealtimeCatalogTrafficPack(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	pub, spy := newRTSpy(t, realtime.ChannelPublic(), realtime.ChannelAdmin())
	e.svc.UseRealtime(pub)

	ctx, _ = seedAdmin(t, ctx, e)
	pack, err := e.svc.CreateTrafficPack(ctx, PackInput{
		Name:         uniqCode(t, "pack"),
		TrafficBytes: BytesPerGB,
		Currency:     "CNY",
		UnitAmount:   decimal.RequireFromString("9.90"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// traffic_packs 由库禁止删除，不清理；名字带随机后缀，不与别的用例冲突

	spy.expect(t, realtime.ChannelPublic(), realtime.TopicPlans, map[string]any{
		"table": rtTablePacks, "op": realtime.OpInsert, "id": pack.ID.String(),
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
