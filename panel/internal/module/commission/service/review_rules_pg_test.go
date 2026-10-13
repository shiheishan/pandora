package service

import (
	"context"
	"errors"
	"testing"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// newReviewEntryIn 同 newReviewEntry，订单币种由调用方给。
func newReviewEntryIn(t *testing.T, e *env, ctx context.Context, referrer, buyer uuid.UUID, total string, cur money.Currency) uuid.UUID {
	t.Helper()
	refer(e, buyer, referrer)
	e.svc.UseSourceMatcher(&fakeSources{same: map[[2]uuid.UUID]bool{{referrer, buyer}: true}})
	defer e.svc.UseSourceMatcher(&fakeSources{})
	o := newPaidOrderIn(t, e, ctx, buyer, total, cur)
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("计提: %v", err)
	}
	var id uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE order_id = ? AND review_required`, o.OrderID).Scan(&id); err != nil {
		t.Fatalf("读待复核条目: %v", err)
	}
	return id
}

// isCheckViolation 判断是不是 CHECK 约束失败（GORM 开了 TranslateError，两种形态都认）。
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.Is(err, gorm.ErrCheckConstraintViolated) || (errors.As(err, &pgErr) && pgErr.Code == "23514")
}

// 只计首单时，作废的佣金也算「已有首单」（用户 10-13 定）：首单佣金作废后，第二单不再计佣。
func TestFirstOrderVoidedStillCounts(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "first_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	id, _ := newReviewEntry(t, e, ctx, referrer, buyer, "100.00")
	if _, err := e.svc.ReviewEntry(adminCtx(ctx, admin), id, false, "同源刷单确认"); err != nil {
		t.Fatal(err)
	}

	second := newPaidOrder(t, e, ctx, buyer, "200.00")
	if err := e.svc.OnOrderPaid(ctx, second); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM commission_entries WHERE referee_user_id = ?`, buyer).Scan(&n); err != nil || n != 1 {
		t.Fatalf("被推荐人有 %d 条佣金（应只有作废的那一条） err=%v", n, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !bal.IsZero() {
		t.Fatalf("第二单不该计佣：待结科目 %s", bal)
	}
}

// 只有作废条目的币种：不进门户汇总的币种选择，也不进提现的币种选择（06c55326）。
func TestVoidedOnlyCurrencyIgnored(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "1.00", "every_order")
	referrer := newUser(t, e, ctx)
	// 站点币种是 testCurrency（USD）；这条佣金是 CNY
	id := newReviewEntryIn(t, e, ctx, referrer, newUser(t, e, ctx), "100.00", money.CNY)

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.Currency != money.CNY {
		t.Fatalf("作废前汇总币种 %s，应为唯一有佣金的 CNY", mc.Summary.Currency)
	}
	if _, err := e.svc.ReviewEntry(adminCtx(ctx, admin), id, false, "同源刷单确认"); err != nil {
		t.Fatal(err)
	}

	mc, err = e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Summary.Currency != testCurrency || !mc.Summary.TotalEarned.IsZero() || !mc.Summary.Pending.IsZero() {
		t.Fatalf("作废后汇总 %+v，币种应回到站点币种 %s", mc.Summary, testCurrency)
	}
	if _, err := e.svc.RequestWithdrawal(ctx, referrer, dec2("10.00"), "alipay a@b.test"); !errors.Is(err, ErrNoCommission) {
		t.Fatalf("只有作废条目时提现应回「没有可提现的佣金」，得到 %v", err)
	}
}

// 作废时推荐人的待结科目不够冲回：回 409，不动账、条目不变。
func TestVoidUnderfundedConflict(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer := newUser(t, e, ctx)
	id, _ := newReviewEntry(t, e, ctx, referrer, newUser(t, e, ctx), "100.00")
	// 模拟账本与条目对不上：直接把推荐人的待结科目清零（只在测试事务里）
	if err := e.exec(ctx, `UPDATE ledger_accounts SET balance = 0 WHERE account_type = ? AND owner_user_id = ? AND currency = ?`,
		string(ledgersvc.UserCommissionPending), referrer, string(testCurrency)); err != nil {
		t.Fatal(err)
	}
	revenueBefore := platformRevenue(t, e, ctx)

	_, err := e.svc.ReviewEntry(adminCtx(ctx, admin), id, false, "同源刷单确认")
	if !errors.Is(err, ErrVoidUnderfunded) || errCode(err) != errs.Conflict {
		t.Fatalf("应回 409 ErrVoidUnderfunded，得到 %v", err)
	}
	var status string
	var review bool
	var voidTxn *uuid.UUID
	if err := e.row(ctx, `SELECT status, review_required, void_txn_id FROM commission_entries WHERE id = ?`, id).
		Scan(&status, &review, &voidTxn); err != nil {
		t.Fatal(err)
	}
	if status != EntryPending || !review || voidTxn != nil {
		t.Fatalf("条目不该变：status=%s review=%v void_txn=%v", status, review, voidTxn)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_voided' AND source_id = ?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不该记冲回：%d 笔 err=%v", n, err)
	}
	if got := platformRevenue(t, e, ctx); !got.Equal(revenueBefore) {
		t.Fatalf("平台收入 %s，不该变（原 %s）", got, revenueBefore)
	}
	if got := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !got.IsZero() {
		t.Fatalf("待结科目 %s，不该变", got)
	}
}

// 00016 的形状约束：每种非法形状在保存点里各试一次，都应被 CHECK 拦下；先用一条合法的作废形状证明基准写法本身能过。
func TestReviewShapeChecks(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	id, _ := newReviewEntry(t, e, ctx, newUser(t, e, ctx), newUser(t, e, ctx), "100.00")

	// 冲回交易借用这条的计提交易（外键只要求交易存在且币种相同）；所有写都在保存点里，回滚后条目原样
	try := func(name, set string, args ...any) error {
		t.Helper()
		if err := e.exec(ctx, `SAVEPOINT shape_check`); err != nil {
			t.Fatal(err)
		}
		err := e.exec(ctx, `UPDATE commission_entries SET `+set+` WHERE id = ?`, append(args, id)...)
		if rerr := e.exec(ctx, `ROLLBACK TO SAVEPOINT shape_check`); rerr != nil {
			t.Fatalf("%s: 回滚保存点: %v", name, rerr)
		}
		return err
	}
	const voidedOK = `status = 'voided', review_required = false, void_txn_id = accrual_txn_id,
		void_reason = '同源刷单确认', reviewed_by = ?, reviewed_at = now()`
	if err := try("合法的作废形状", voidedOK, admin); err != nil {
		t.Fatalf("基准写法应能通过: %v", err)
	}
	if err := try("合法的通过形状", `review_required = false, reviewed_by = ?, reviewed_at = now()`, admin); err != nil {
		t.Fatalf("通过形状应能通过: %v", err)
	}

	bad := []struct {
		name string
		set  string
		args []any
	}{
		{"voided 缺冲回交易", `status = 'voided', review_required = false, void_reason = '同源刷单确认', reviewed_by = ?, reviewed_at = now()`, []any{admin}},
		{"voided 缺作废原因", `status = 'voided', review_required = false, void_txn_id = accrual_txn_id, reviewed_by = ?, reviewed_at = now()`, []any{admin}},
		{"复核人有、复核时间空", `review_required = false, reviewed_by = ?`, []any{admin}},
		{"复核时间有、复核人空", `review_required = false, reviewed_at = now()`, nil},
		{"有复核原因却没复核过", `review_required = false`, nil},
		{"作废原因只有 4 个字", `status = 'voided', review_required = false, void_txn_id = accrual_txn_id,
			void_reason = '四个字啊', reviewed_by = ?, reviewed_at = now()`, []any{admin}},
	}
	for _, c := range bad {
		if err := try(c.name, c.set, c.args...); !isCheckViolation(err) {
			t.Errorf("%s：应被 CHECK 拦下，得到 %v", c.name, err)
		}
	}

	var status string
	var review bool
	if err := e.row(ctx, `SELECT status, review_required FROM commission_entries WHERE id = ?`, id).Scan(&status, &review); err != nil {
		t.Fatal(err)
	}
	if status != EntryPending || !review {
		t.Fatalf("保存点回滚后条目应原样：status=%s review=%v", status, review)
	}
}
