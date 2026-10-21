package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// newReviewEntry 让 referrer 通过 buyer 的一张订单拿到一条待复核的佣金（同源嫌疑），返回条目 id 与佣金额。
func newReviewEntry(t *testing.T, e *env, ctx context.Context, referrer, buyer uuid.UUID, total string) (uuid.UUID, decimal.Decimal) {
	t.Helper()
	refer(e, buyer, referrer)
	e.svc.UseSourceMatcher(&fakeSources{same: map[[2]uuid.UUID]bool{{referrer, buyer}: true}})
	defer e.svc.UseSourceMatcher(&fakeSources{})
	o := newPaidOrder(t, e, ctx, buyer, total)
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatalf("计提: %v", err)
	}
	var id uuid.UUID
	var amt decimal.Decimal
	var review bool
	if err := e.row(ctx, `SELECT id, commission_amount, review_required FROM commission_entries WHERE order_id = ?`, o.OrderID).
		Scan(&id, &amt, &review); err != nil {
		t.Fatal(err)
	}
	if !review {
		t.Fatal("同源的佣金应待复核")
	}
	return id, amt
}

// platformRevenue 读平台收入 main 科目的余额缓存（没有科目时为 0）。
func platformRevenue(t *testing.T, e *env, ctx context.Context) decimal.Decimal {
	t.Helper()
	var bal decimal.Decimal
	if err := e.row(ctx, `SELECT COALESCE((SELECT balance FROM ledger_accounts
		WHERE account_type = ? AND owner_ref = ?), 0)`,
		string(ledgersvc.PlatformRevenue), ledgersvc.RefMain).Scan(&bal); err != nil {
		t.Fatal(err)
	}
	return bal
}

// assertNoDrift 断言每个科目的余额缓存等于它全部分录的合计（同 order_e2e 用例的口径）。
func assertNoDrift(t *testing.T, e *env, ctx context.Context) {
	t.Helper()
	var agg sql.NullString
	err := e.row(ctx, `
SELECT string_agg(d.id, ',') FROM (
  SELECT a.id::text AS id
  FROM ledger_accounts a
  LEFT JOIN ledger_entries e ON e.account_id = a.id
  GROUP BY a.id, a.account_type, a.balance
  HAVING a.balance <> (
    CASE WHEN a.account_type IN ('channel_cash', 'platform_fee_expense', 'platform_promo_expense', 'suspense') THEN
      COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
    ELSE
      COALESCE(SUM(CASE WHEN e.direction = 'credit' THEN e.amount ELSE 0 END), 0)
      - COALESCE(SUM(CASE WHEN e.direction = 'debit' THEN e.amount ELSE 0 END), 0)
    END
  )
) d`).Scan(&agg)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if agg.Valid && strings.TrimSpace(agg.String) != "" {
		t.Fatalf("科目余额缓存与分录合计不一致: %s", agg.String)
	}
}

// 通过：清 review_required、记复核人与时间、原因留作历史；冻结期已过的，下一轮解冻任务就解冻。
func TestApproveEntryThenSettle(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	id, amt := newReviewEntry(t, e, ctx, referrer, buyer, "100.00")

	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if len(mc.Entries) != 1 || !mc.Entries[0].UnderReview || mc.Entries[0].Status != EntryPending {
		t.Fatalf("待复核时 MyCommission 条目不对: %+v", mc.Entries)
	}

	// 待复核的不参与自动解冻
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	status, err := e.svc.ReviewEntry(adminCtx(ctx, admin), id, true, "")
	if err != nil || status != EntryPending {
		t.Fatalf("通过: status=%s err=%v", status, err)
	}
	var review bool
	var reason *string
	var by *uuid.UUID
	var at *time.Time
	if err := e.row(ctx, `SELECT review_required, review_reason, reviewed_by, reviewed_at FROM commission_entries WHERE id = ?`, id).
		Scan(&review, &reason, &by, &at); err != nil {
		t.Fatal(err)
	}
	if review || reason == nil || *reason == "" || by == nil || *by != admin || at == nil {
		t.Fatalf("通过后: review=%v reason=%v by=%v at=%v", review, reason, by, at)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionEntryApproved, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("审计 %d err=%v", n, err)
	}

	mcApproved, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if len(mcApproved.Entries) != 1 || mcApproved.Entries[0].UnderReview {
		t.Fatalf("复核通过后 UnderReview 应为 false: %+v", mcApproved.Entries)
	}

	st, err := e.svc.SettleDue(ctx, SettleBatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	if st.Settled < 1 {
		t.Fatalf("统计 %+v", st)
	}
	if err := e.row(ctx, `SELECT status FROM commission_entries WHERE id = ?`, id).Scan(&status); err != nil || status != EntryAvailable {
		t.Fatalf("通过后应被解冻: status=%s err=%v", status, err)
	}
	if bal := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionAvailable); !bal.Equal(amt) {
		t.Fatalf("可提现佣金科目 %s，应为 %s", bal, amt)
	}
}

// 作废：同一事务记 commission_voided 冲回计提，待结科目减少、平台收入回升，账本不漂移；汇总不再计入。
func TestVoidEntryReversesAccrual(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	id, amt := newReviewEntry(t, e, ctx, referrer, buyer, "100.00")

	pendingBefore := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending)
	revenueBefore := platformRevenue(t, e, ctx)
	if !pendingBefore.Equal(amt) {
		t.Fatalf("计提后待结科目 %s，应为 %s", pendingBefore, amt)
	}

	status, err := e.svc.ReviewEntry(adminCtx(ctx, admin), id, false, "  同源刷单，已核实  ")
	if err != nil || status != EntryVoided {
		t.Fatalf("作废: status=%s err=%v", status, err)
	}
	if got := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !got.Equal(pendingBefore.Sub(amt)) {
		t.Fatalf("待结科目 %s，应为 %s", got, pendingBefore.Sub(amt))
	}
	if got := platformRevenue(t, e, ctx); !got.Equal(revenueBefore.Add(amt)) {
		t.Fatalf("平台收入 %s，应为 %s", got, revenueBefore.Add(amt))
	}

	var st string
	var review bool
	var voidTxn *uuid.UUID
	var voidReason *string
	var by *uuid.UUID
	if err := e.row(ctx, `SELECT status, review_required, void_txn_id, void_reason, reviewed_by FROM commission_entries WHERE id = ?`, id).
		Scan(&st, &review, &voidTxn, &voidReason, &by); err != nil {
		t.Fatal(err)
	}
	if st != EntryVoided || review || voidTxn == nil || voidReason == nil || *voidReason != "同源刷单，已核实" || by == nil || *by != admin {
		t.Fatalf("作废后: status=%s review=%v txn=%v reason=%v by=%v", st, review, voidTxn, voidReason, by)
	}
	// 冲回交易：类型、来源、借贷平衡
	var kind, srcType string
	var srcID uuid.UUID
	var net decimal.Decimal
	if err := e.row(ctx, `
SELECT t.kind, t.source_type, t.source_id,
       (SELECT COALESCE(sum(CASE WHEN le.direction = 'debit' THEN le.amount ELSE -le.amount END), 0)
          FROM ledger_entries le WHERE le.transaction_id = t.id)
  FROM ledger_transactions t WHERE t.id = ?`, *voidTxn).Scan(&kind, &srcType, &srcID, &net); err != nil {
		t.Fatal(err)
	}
	if kind != string(ledgersvc.KindCommissionVoided) || srcType != SourceCommissionEntry || srcID != id || !net.IsZero() {
		t.Fatalf("冲回交易 kind=%s source=%s/%s 借贷差 %s", kind, srcType, srcID, net)
	}
	assertNoDrift(t, e, ctx)

	var n int
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND data->>'reason' = ?`,
		ActionEntryVoided, id, "同源刷单，已核实").Scan(&n); err != nil || n != 1 {
		t.Fatalf("审计 %d err=%v", n, err)
	}
	// 汇总不计作废的；解冻任务碰不到它
	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.Summary.TotalEarned.IsZero() || !mc.Summary.Pending.IsZero() || mc.Summary.Orders != 0 {
		t.Fatalf("作废后汇总 %+v", mc.Summary)
	}
	if _, err := e.svc.SettleDue(ctx, SettleBatchDefault); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT status FROM commission_entries WHERE id = ?`, id).Scan(&st); err != nil || st != EntryVoided {
		t.Fatalf("作废后被解冻任务改了: %s err=%v", st, err)
	}
}

// 不在待复核状态的条目（正常条目、已通过、已作废）通过或作废都回 409；不存在回 404。
func TestReviewEntryNotReviewable(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 30, "100.00", "every_order")
	actx := adminCtx(ctx, admin)

	// 正常条目（不同源）：不需复核
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	refer(e, buyer, referrer)
	o := newPaidOrder(t, e, ctx, buyer, "100.00")
	if err := e.svc.OnOrderPaid(ctx, o); err != nil {
		t.Fatal(err)
	}
	var normal uuid.UUID
	if err := e.row(ctx, `SELECT id FROM commission_entries WHERE order_id = ?`, o.OrderID).Scan(&normal); err != nil {
		t.Fatal(err)
	}
	for _, approve := range []bool{true, false} {
		if _, err := e.svc.ReviewEntry(actx, normal, approve, "不该能处理这条"); errCode(err) != errs.Conflict {
			t.Fatalf("正常条目 approve=%v 应 409，得到 %v", approve, err)
		}
	}

	// 已通过的再通过、再作废
	approved, _ := newReviewEntry(t, e, ctx, newUser(t, e, ctx), newUser(t, e, ctx), "100.00")
	if _, err := e.svc.ReviewEntry(actx, approved, true, "核实无误"); err != nil {
		t.Fatal(err)
	}
	for _, approve := range []bool{true, false} {
		if _, err := e.svc.ReviewEntry(actx, approved, approve, "第二次处理"); errCode(err) != errs.Conflict {
			t.Fatalf("已通过的 approve=%v 应 409，得到 %v", approve, err)
		}
	}

	// 已作废的再作废、再通过：只有一笔冲回
	voided, _ := newReviewEntry(t, e, ctx, newUser(t, e, ctx), newUser(t, e, ctx), "100.00")
	if _, err := e.svc.ReviewEntry(actx, voided, false, "同源刷单确认"); err != nil {
		t.Fatal(err)
	}
	for _, approve := range []bool{false, true} {
		if _, err := e.svc.ReviewEntry(actx, voided, approve, "同源刷单确认"); errCode(err) != errs.Conflict {
			t.Fatalf("已作废的 approve=%v 应 409，得到 %v", approve, err)
		}
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_voided' AND source_id = ?`, voided).Scan(&n); err != nil || n != 1 {
		t.Fatalf("冲回 %d 笔 err=%v", n, err)
	}

	if _, err := e.svc.ReviewEntry(actx, uuid.New(), true, ""); errCode(err) != errs.NotFound {
		t.Fatalf("不存在应 404，得到 %v", err)
	}
	// 没有登录主体
	if _, err := e.svc.ReviewEntry(ctx, approved, true, ""); err == nil {
		t.Fatal("没有主体不该成功")
	}
}

// 作废原因去掉首尾空白后不少于 5 个字，否则 422 且不动账；通过的原因可空。
func TestVoidReasonTooShort(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	actx := adminCtx(ctx, admin)
	referrer := newUser(t, e, ctx)
	id, amt := newReviewEntry(t, e, ctx, referrer, newUser(t, e, ctx), "100.00")

	for _, reason := range []string{"", "   ", "四个字啊", "  abcd  "} {
		_, err := e.svc.ReviewEntry(actx, id, false, reason)
		if errCode(err) != errs.ValidationFailed || errs.As(err).Fields["reason"] == "" {
			t.Fatalf("原因 %q 应 422，得到 %v", reason, err)
		}
	}
	if _, err := e.svc.ReviewEntry(actx, id, false, strings.Repeat("长", MaxEntryReviewRunes+1)); errCode(err) != errs.ValidationFailed {
		t.Fatalf("超长原因应 422，得到 %v", err)
	}
	if got := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !got.Equal(amt) {
		t.Fatalf("422 不该动账：待结科目 %s", got)
	}
	if _, err := e.svc.ReviewEntry(actx, id, true, ""); err != nil {
		t.Fatalf("通过可以不填原因: %v", err)
	}
}

// 待复核列表：按计提时间倒序键集翻页，邮箱批量取回；坏的翻页位置回 422。
func TestAdminReviewEntriesPaging(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "100.00", "every_order")
	referrer := newUser(t, e, ctx)
	var ids []uuid.UUID
	buyers := map[uuid.UUID]uuid.UUID{}
	for i := 0; i < 3; i++ {
		b := newUser(t, e, ctx)
		id, _ := newReviewEntry(t, e, ctx, referrer, b, "100.00")
		ids = append(ids, id)
		buyers[id] = b
	}
	// 把这三条排到最前面（库里可能还有别的用例留下的待复核条目），ids[2] 最新
	for i, id := range ids {
		if err := e.exec(ctx, `UPDATE commission_entries SET created_at = ? WHERE id = ?`,
			time.Date(2100, 1, 1+i, 0, 0, 0, 0, time.UTC), id); err != nil {
			t.Fatal(err)
		}
	}

	p1, err := e.svc.AdminReviewEntries(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Entries) != 2 || p1.Entries[0].ID != ids[2] || p1.Entries[1].ID != ids[1] || p1.NextCursor == "" {
		t.Fatalf("第一页 %+v", p1)
	}
	row := p1.Entries[0]
	if row.ReferrerEmail != e.refs.emails[referrer] || row.RefereeEmail != e.refs.emails[buyers[ids[2]]] ||
		row.ReviewReason == "" || row.ReviewSource != ReviewSourceSameSource || !row.Amount.Equal(dec2("10.00")) || row.OrderNo == "" {
		t.Fatalf("行 %+v", row)
	}
	p2, err := e.svc.AdminReviewEntries(ctx, p1.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Entries) == 0 || p2.Entries[0].ID != ids[0] {
		t.Fatalf("第二页 %+v", p2)
	}

	// 通过一条后不再出现在列表里
	if _, err := e.svc.ReviewEntry(adminCtx(ctx, admin), ids[2], true, ""); err != nil {
		t.Fatal(err)
	}
	p1, err = e.svc.AdminReviewEntries(ctx, "", 1)
	if err != nil || len(p1.Entries) != 1 || p1.Entries[0].ID != ids[1] {
		t.Fatalf("通过后第一页 %+v err=%v", p1, err)
	}

	for _, bad := range []string{"%%%", "bm90LWEtY3Vyc29y"} {
		if _, err := e.svc.AdminReviewEntries(ctx, bad, 10); errCode(err) != errs.ValidationFailed {
			t.Fatalf("坏翻页位置 %q 应 422，得到 %v", bad, err)
		}
	}
	if _, err := e.svc.AdminReviewEntries(ctx, "", -1); errCode(err) != errs.ValidationFailed {
		t.Fatalf("负 limit 应 422，得到 %v", err)
	}
}

// 两个管理员真并发作废同一条（各自提交）：只有一个成功，另一个 409；只有一笔冲回，账本不漂移。
func TestConcurrentVoidOnlyOnce(t *testing.T) {
	e, _ := newEnv(t)
	e.cleanCommitted(t)
	ctx := commitCtx()
	admin := newUser(t, e, ctx)
	prev, err := e.settings.Commission(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.settings.SetCommission(adminCtx(commitCtx(), admin), settingssvc.CommissionUpdate{
			RatePercent: &prev.RatePercent, FreezeDays: &prev.FreezeDays, MinWithdraw: &prev.MinWithdraw, Scope: &prev.Scope,
		}); err != nil {
			t.Errorf("还原分销参数: %v", err)
		}
	})
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer := newUser(t, e, ctx)
	id, amt := newReviewEntry(t, e, ctx, referrer, newUser(t, e, ctx), "100.00")
	revenueBefore := platformRevenue(t, e, ctx)

	const workers = 4
	var wg sync.WaitGroup
	results := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.svc.ReviewEntry(adminCtx(commitCtx(), admin), id, false, "同源刷单确认")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errCode(err) == errs.Conflict:
		default:
			t.Fatalf("并发作废出现意外错误: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("成功 %d 次，应恰好 1 次", ok)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_voided' AND source_id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("冲回 %d 笔 err=%v", n, err)
	}
	if got := accountBalance(t, e, ctx, referrer, ledgersvc.UserCommissionPending); !got.IsZero() {
		t.Fatalf("待结科目 %s", got)
	}
	if got := platformRevenue(t, e, ctx); !got.Equal(revenueBefore.Add(amt)) {
		t.Fatalf("平台收入 %s，应为 %s", got, revenueBefore.Add(amt))
	}
	assertNoDrift(t, e, ctx)
}
