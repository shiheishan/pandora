package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// committedLimitedPublished 提交态环境下建管理员并发布限量套餐，返回套餐 ID 与带主体的 ctx。
func committedLimitedPublished(t *testing.T, e *env, ctx context.Context, stockTotal int) (context.Context, uuid.UUID) {
	t.Helper()
	ctx, _ = seedAdmin(t, ctx, e)
	st := stockTotal
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{StockTotal: &st})
	return ctx, plan.ID
}

func TestStockConcurrentReserve(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, planID := committedLimitedPublished(t, e, ctx, 5)

	const workers = 20
	var (
		wg      sync.WaitGroup
		okCount int
		soldOut int
		mu      sync.Mutex
	)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
				_, err := e.svc.ReserveStock(ctx, planID, 1)
				return err
			})
			mu.Lock()
			switch {
			case err == nil:
				okCount++
			case errors.Is(err, ErrSoldOut):
				soldOut++
			default:
				t.Errorf("ReserveStock: %v", err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if okCount != 5 {
		t.Fatalf("期望 5 次预留成功，得到 %d", okCount)
	}
	if soldOut != 15 {
		t.Fatalf("期望 15 次售罄，得到 %d", soldOut)
	}
	if n := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, planID); n != 5 {
		t.Fatalf("stock_reserved 期望 5，得到 %d", n)
	}
}

func TestStockReleaseCaptureAndReleaseMismatch(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, planID := committedLimitedPublished(t, e, ctx, 5)

	for i := 0; i < 5; i++ {
		if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
			ok, err := e.svc.ReserveStock(ctx, planID, 1)
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
	}

	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		return e.svc.ReleaseStock(ctx, planID, 2)
	}); err != nil {
		t.Fatal(err)
	}
	if n := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, planID); n != 3 {
		t.Fatalf("Release 后 stock_reserved 期望 3，得到 %d", n)
	}

	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		return e.svc.CaptureStock(ctx, planID, 3)
	}); err != nil {
		t.Fatal(err)
	}
	if reserved := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, planID); reserved != 0 {
		t.Fatalf("Capture 后 stock_reserved 期望 0，得到 %d", reserved)
	}
	if sold := scanInt64(t, ctx, e, `SELECT stock_sold FROM plans WHERE id = ?`, planID); sold != 3 {
		t.Fatalf("Capture 后 stock_sold 期望 3，得到 %d", sold)
	}

	var releaseErr error
	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		releaseErr = e.svc.ReleaseStock(ctx, planID, 1)
		return releaseErr
	}); err == nil {
		t.Fatal("预留不足时 Release 应失败")
	}
	if errCode(releaseErr) != errs.Internal {
		t.Fatalf("期望 internal_error，得到 %s: %v", errCode(releaseErr), releaseErr)
	}
}

func TestStockUnlimitedReserveNoOp(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{StockTotal: nil})

	beforeReserved := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, plan.ID)
	beforeSold := scanInt64(t, ctx, e, `SELECT stock_sold FROM plans WHERE id = ?`, plan.ID)

	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := e.svc.ReserveStock(ctx, plan.ID, 1)
		if err != nil {
			return err
		}
		if ok {
			return errors.New("不限量套餐不应返回 reserved=true")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if after := scanInt64(t, ctx, e, `SELECT stock_reserved FROM plans WHERE id = ?`, plan.ID); after != beforeReserved {
		t.Fatalf("stock_reserved 不应变化：前 %d 后 %d", beforeReserved, after)
	}
	if after := scanInt64(t, ctx, e, `SELECT stock_sold FROM plans WHERE id = ?`, plan.ID); after != beforeSold {
		t.Fatalf("stock_sold 不应变化：前 %d 后 %d", beforeSold, after)
	}
}

func TestStockReserveNotFound(t *testing.T) {
	e, ctx := newEnvCommitted(t)
	ctx, _ = seedAdmin(t, ctx, e)
	missing := uuid.New()

	var gotErr error
	if err := e.svc.tx.Do(ctx, func(ctx context.Context) error {
		_, err := e.svc.ReserveStock(ctx, missing, 1)
		gotErr = err
		return err
	}); err == nil {
		t.Fatal("不存在的套餐应返回错误")
	}
	if !errors.Is(gotErr, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", gotErr)
	}
}
