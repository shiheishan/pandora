package service

// 下单快照的守卫：建单加锁与改价互斥、续费价格必须属于订阅的套餐、
// 当前版本不是 published 时按没有版本处理、已删用户浏览门户按匿名。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 一个事务 Offer(Lock) 持有套餐与价格的 FOR SHARE 期间，另一提交态事务 UpdatePrices 应被阻塞直到前者结束。
func TestOfferLockBlocksUpdatePrices(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)

	plan := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name: "锁阻塞改价",
		Tiers: []TierInput{
			{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: decimal.RequireFromString("9.90")},
			{BillingInterval: IntervalMonth, IntervalCount: 3, UnitAmount: decimal.RequireFromString("27.00")},
		},
	})
	priceID := plan.Prices[0].ID
	buyer := seedUser(t, ctx, e, nil)

	var (
		locked   sync.WaitGroup
		release  = make(chan struct{})
		holdDone sync.WaitGroup
	)
	locked.Add(1)
	holdDone.Add(1)

	go func() {
		defer holdDone.Done()
		holdOK := false
		defer func() {
			if !holdOK {
				locked.Done()
			}
		}()
		err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
			_, err := e.svc.Offer(ctx, priceID, OfferRequest{Use: UseNew, UserID: &buyer, Lock: true})
			if err != nil {
				return err
			}
			holdOK = true
			locked.Done()
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				return errors.New("测试超时：未收到释放信号")
			}
			return nil
		})
		if err != nil {
			t.Errorf("持锁 Offer: %v", err)
		}
	}()

	locked.Wait()

	updateErr := make(chan error, 1)
	go func() {
		detail, err := e.svc.GetPlan(ctx, plan.ID)
		if err != nil {
			updateErr <- err
			return
		}
		_, err = e.svc.UpdatePrices(ctx, plan.ID, PricesUpdate{
			ExpectedRowVersion: detail.RowVersion,
			Tiers: []TierChange{
				{BillingInterval: IntervalMonth, IntervalCount: 1, UnitAmount: ptrDecOfferGuard("10.00")},
				{BillingInterval: IntervalMonth, IntervalCount: 3, UnitAmount: ptrDecOfferGuard("28.00")},
			},
		})
		updateErr <- err
	}()

	select {
	case err := <-updateErr:
		close(release)
		holdDone.Wait()
		t.Fatalf("持锁期间 UpdatePrices 不应立刻完成: %v", err)
	case <-time.After(400 * time.Millisecond):
	}

	close(release)
	holdDone.Wait()

	select {
	case err := <-updateErr:
		if err != nil {
			t.Fatalf("释放锁后 UpdatePrices 应成功: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("释放锁后 UpdatePrices 超时")
	}
}

func ptrDecOfferGuard(s string) *decimal.Decimal {
	d := decimal.RequireFromString(s)
	return &d
}

func TestOfferPriceMustBelongToPlan(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	a := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "套餐甲"})
	b := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "套餐乙"})
	priceB := b.Prices[0].ID
	buyer := seedUser(t, ctx, e, nil)

	_, err := e.svc.Offer(ctx, priceB, OfferRequest{Use: UseRenewal, UserID: &buyer, PlanID: &a.ID})
	if !errors.Is(err, ErrRenewPriceGone) {
		t.Fatalf("续费拿别的套餐的价格应 ErrRenewPriceGone，得到 %v", err)
	}
	_, err = e.svc.Offer(ctx, priceB, OfferRequest{Use: UseNew, UserID: &buyer, PlanID: &a.ID})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("新购价格与套餐不符应 ErrNotFound，得到 %v", err)
	}
	_, err = e.svc.Offer(ctx, priceB, OfferRequest{Use: UseChange, UserID: &buyer, PlanID: &a.ID, Lock: true})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("换套餐价格与套餐不符应 ErrNotFound，得到 %v", err)
	}

	off, err := e.svc.Offer(ctx, priceB, OfferRequest{Use: UseRenewal, UserID: &buyer, PlanID: &b.ID})
	if err != nil {
		t.Fatalf("价格属于套餐时续费应成功：%v", err)
	}
	if off.Plan.ID != b.ID || off.Price.ID != priceB {
		t.Fatalf("快照对不上：plan %s price %s", off.Plan.ID, off.Price.ID)
	}
}

func TestOfferTreatsDraftCurrentVersionAsNone(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "草稿当前"})
	priceID := plan.Prices[0].ID
	buyer := seedUser(t, ctx, e, nil)

	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UpdateUsage(ctx, plan.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: int64PtrOfferGuard(20 * 1024 * 1024 * 1024)},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err = e.svc.GetPlan(ctx, plan.ID)
	if err != nil || detail.DraftVersionID == nil {
		t.Fatalf("应有草稿版本: err=%v draft=%v", err, detail.DraftVersionID)
	}
	if err := e.exec(ctx, `UPDATE plans SET current_version_id = ? WHERE id = ?`, *detail.DraftVersionID, plan.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.Offer(ctx, priceID, OfferRequest{Use: UseNew, UserID: &buyer}); !errors.Is(err, ErrNoPublishedVersion) {
		t.Fatalf("Offer 期望 ErrNoPublishedVersion，得到 %v", err)
	}
	if _, err := e.svc.PlanOffers(ctx, plan.ID, OfferRequest{Use: UseNew, UserID: &buyer}); !errors.Is(err, ErrNoPublishedVersion) {
		t.Fatalf("PlanOffers 期望 ErrNoPublishedVersion，得到 %v", err)
	}
	list, err := e.svc.ListOffers(ctx, OfferRequest{Use: UseChange, UserID: &buyer})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range list {
		if o.Plan.ID == plan.ID {
			t.Fatal("ListOffers 不应列出当前版本是草稿的套餐")
		}
	}
	portal, err := e.svc.PortalCatalog(ctx, &buyer)
	if err != nil {
		t.Fatal(err)
	}
	if portalHasPlan(portal, plan.ID) {
		t.Fatal("门户不应列出当前版本是草稿的套餐")
	}
}

func int64PtrOfferGuard(n int64) *int64 { return &n }

func TestPortalCatalogDeletedUserAsAnonymous(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	public := publishedPlan(t, ctx, e, publishedPlanOpts{Name: "公开"})
	authOnly := publishedPlan(t, ctx, e, publishedPlanOpts{
		Name:       "仅登录",
		Visibility: VisibilityAuthenticated,
	})

	ghost := uuid.New()
	e.users.deleted = map[uuid.UUID]bool{ghost: true}

	list, err := e.svc.PortalCatalog(ctx, &ghost)
	if err != nil {
		t.Fatalf("已删用户浏览门户应按匿名，得到 %v", err)
	}
	if !portalHasPlan(list, public.ID) {
		t.Fatal("匿名应看到公开套餐")
	}
	if portalHasPlan(list, authOnly.ID) {
		t.Fatal("匿名不应看到仅登录可见的套餐")
	}

	_, err = e.svc.Offer(ctx, public.Prices[0].ID, OfferRequest{Use: UseNew, UserID: &ghost})
	if errCode(err) != errs.NotFound {
		t.Fatalf("已删用户下单应 404，得到 %v", err)
	}
}
