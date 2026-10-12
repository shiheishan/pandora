package service

// 下单快照的守卫：建单加锁与库存不死锁、续费价格必须属于订阅的套餐、
// 当前版本不是 published 时按没有版本处理、已删用户浏览门户按匿名。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 多笔「Offer(Lock) + ReserveStock」并发：全部成功或售罄，不能死锁（40P01）。
func TestOfferLockThenReserveNoDeadlock(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)
	const stock = 5
	st := stock
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{StockTotal: &st})
	if len(plan.Prices) == 0 {
		t.Fatal("套餐没有价格")
	}
	priceID := plan.Prices[0].ID
	buyer := seedUser(t, ctx, e, nil)

	const workers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		okCount int
		soldOut int
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
				off, err := e.svc.Offer(ctx, priceID, OfferRequest{Use: UseNew, UserID: &buyer, Lock: true})
				if err != nil {
					return err
				}
				_, err = e.svc.ReserveStock(ctx, off.Plan.ID, 1)
				return err
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrSoldOut):
				soldOut++
			default:
				t.Errorf("Offer(Lock)+ReserveStock: %v", err)
			}
		}()
	}
	wg.Wait()

	if okCount != stock || soldOut != workers-stock {
		t.Fatalf("期望 %d 成功、%d 售罄，得到 %d、%d", stock, workers-stock, okCount, soldOut)
	}
	if n := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, plan.ID); n != stock {
		t.Fatalf("stock_reserved 期望 %d，得到 %d", stock, n)
	}
}

func TestOfferPriceMustBelongToPlan(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	a := publishedPlan(t, ctx, e, publishedPlanOpts{})
	b := publishedPlan(t, ctx, e, publishedPlanOpts{})
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

// 当前版本被直写成草稿（库里不兜「已发布」）时，读侧一律按没有发布版本处理。
func TestOfferTreatsDraftCurrentVersionAsNone(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	priceID := plan.Prices[0].ID
	buyer := seedUser(t, ctx, e, nil)

	draft, err := e.svc.CreateVersion(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE plans SET current_version_id = ? WHERE id = ?`, draft.ID, plan.ID); err != nil {
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

// 令牌还有效但用户已被删：门户目录按匿名处理，下单快照仍报错。
func TestPortalCatalogDeletedUserAsAnonymous(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	public := publishedPlan(t, ctx, e, publishedPlanOpts{})
	authOnly := publishedPlan(t, ctx, e, publishedPlanOpts{Visibility: VisibilityAuthenticated})

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
