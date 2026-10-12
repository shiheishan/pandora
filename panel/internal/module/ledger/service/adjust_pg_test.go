package service

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAdjustCredit(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)
	reason := "补偿线路故障"

	res, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID:   user,
		Amount:   dec("12.50"),
		Currency: "CNY",
		Reason:   reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Currency != money.CNY || !res.Balance.Equal(dec("12.50")) || res.TxnID == uuid.Nil {
		t.Fatalf("结果不对: %+v", res)
	}
	if !e.balanceOf(t, ctx, user, money.CNY).Equal(dec("12.50")) {
		t.Fatalf("用户余额应为 12.50")
	}
	susp := PlatformAccount(Suspense, RefManualAdjust)
	if !e.accountBalance(t, ctx, susp, money.CNY).Equal(dec("12.50")) {
		t.Fatalf("suspense/manual_adjust 应为 12.50")
	}

	var kind, memo, actorKind, sourceType string
	var sourceID sql.NullString
	if err := e.row(ctx, `
SELECT kind, memo, actor_kind, COALESCE(source_type, ''), source_id::text
FROM ledger_transactions WHERE id = ?`, res.TxnID).Scan(&kind, &memo, &actorKind, &sourceType, &sourceID); err != nil {
		t.Fatal(err)
	}
	if kind != string(KindBalanceAdjusted) || memo != reason || actorKind != "admin" || sourceType != SourceUser || !sourceID.Valid || sourceID.String != user.String() {
		t.Fatalf("交易元数据不对: kind=%s memo=%s actor=%s source=%s/%v", kind, memo, actorKind, sourceType, sourceID)
	}

	var auditN int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = 'balance.adjusted' AND resource_id = ?`, user).Scan(&auditN); err != nil {
		t.Fatal(err)
	}
	if auditN != 1 {
		t.Fatalf("审计应为 1 条，得 %d", auditN)
	}
	e.assertNoDrift(t, ctx)
}

func TestAdjustDebit(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)
	cur := money.CNY

	if _, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("10.00"), Currency: string(cur), Reason: "先加十个字原因",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("-4.00"), Currency: string(cur), Reason: "再减四个字原因",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Balance.Equal(dec("6.00")) {
		t.Fatalf("余额应为 6，得 %s", res.Balance)
	}
	e.assertNoDrift(t, ctx)
}

func TestAdjustOverdraft(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)
	cur := money.CNY

	if _, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("3.00"), Currency: string(cur), Reason: "初始加三个字",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("-5.00"), Currency: string(cur), Reason: "扣减五个字原因",
	})
	if codeOf(err) != errs.Conflict {
		t.Fatalf("code 应为 conflict，得 %s %v", codeOf(err), err)
	}
	if errs.As(err).Message != "余额不足，无法扣减这么多" {
		t.Fatalf("文案不对: %q", errs.As(err).Message)
	}
	if !e.balanceOf(t, ctx, user, cur).Equal(dec("3.00")) {
		t.Fatalf("余额应仍为 3")
	}
	var auditN int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = 'balance.adjusted' AND resource_id = ?`, user).Scan(&auditN); err != nil {
		t.Fatal(err)
	}
	if auditN != 0 {
		t.Fatalf("失败调账不应写审计，得 %d", auditN)
	}
}

func TestAdjustDefaultCurrency(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)

	def, err := e.svc.DefaultCurrency(ctx)
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("1.00"), Currency: "", Reason: "默认币种五个字",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Currency != def {
		t.Fatalf("币种应为站点缺省 %s，得 %s", def, res.Currency)
	}
	if def != money.USD {
		t.Fatalf("测试环境站点缺省应为 USD，得 %s", def)
	}
}

func TestAdjustValidation(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	user := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)

	_, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: decimal.Zero, Currency: "CNY", Reason: "原因够五个字",
	})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("金额 0: code=%s err=%v", codeOf(err), err)
	}

	_, err = e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("1.00"), Currency: "CNY", Reason: "太短",
	})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("原因太短: code=%s", codeOf(err))
	}
	pe := errs.As(err)
	if pe.Fields == nil || pe.Fields["reason"] == "" {
		t.Fatalf("应有 reason 字段错误: %v", pe.Fields)
	}

	_, err = e.svc.AdjustBalance(ctx, Adjustment{
		UserID: user, Amount: dec("1.00"), Currency: "EUR", Reason: "币种错误五个字",
	})
	if codeOf(err) != errs.ValidationFailed {
		t.Fatalf("EUR: code=%s", codeOf(err))
	}
	pe = errs.As(err)
	if pe.Fields == nil || pe.Fields["currency"] == "" {
		t.Fatalf("应有 currency 字段错误: %v", pe.Fields)
	}
}

func TestAdjustAuth(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	adj := Adjustment{UserID: user, Amount: dec("1.00"), Currency: "CNY", Reason: "权限测试五个字"}

	_, err := e.svc.AdjustBalance(ctx, adj)
	if codeOf(err) != errs.Unauthorized {
		t.Fatalf("无主体应 unauthorized，得 %s %v", codeOf(err), err)
	}

	_, err = e.svc.AdjustBalance(asUser(ctx, user), adj)
	if codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体应 forbidden，得 %s %v", codeOf(err), err)
	}
}

func TestAdjustUnknownUser(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkUser(t, ctx)
	ctx = asAdmin(ctx, admin)
	ghost := uuid.New()

	_, err := e.svc.AdjustBalance(ctx, Adjustment{
		UserID: ghost, Amount: dec("1.00"), Currency: "CNY", Reason: "不存在用户五个",
	})
	if !errors.Is(err, ErrUserNotFound) && codeOf(err) != errs.NotFound {
		t.Fatalf("期望 not_found，得 %s %v", codeOf(err), err)
	}
}
