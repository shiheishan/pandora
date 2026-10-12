package service

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 待用户支付的人工单低于站点最低额：预览标 below_minimum，建单回 422 落在 settlement 上；赠送不受限。
func TestManualOrderBelowMinimum(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	e.mkMinPay(t, ctx, 5000)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month", nil, nil)

	pv, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planID, PriceID: &priceID})
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	mustEq(t, "最低额", pv.MinPayment, dec("50"))
	if len(pv.Options) != 1 || !pv.Options[0].BelowMinimum {
		t.Fatalf("预览 %+v", pv.Options)
	}
	_, err = e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason, Settlement: SettlementPending,
	})
	if codeOf(err) != errs.ValidationFailed || errs.As(err).Fields["settlement"] == "" {
		t.Fatalf("低于最低额应 422 settlement，得到 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE created_by = ?`, admin); n != 0 {
		t.Fatalf("不该留下订单，有 %d 张", n)
	}
	if c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: planID, PriceID: priceID, Reason: manualReason}); err != nil ||
		c.Status != StatusFulfilled {
		t.Fatalf("赠送不受最低额限制: %+v %v", c, err)
	}
}

// 线下已收款换套餐：剩余价值 15.00 抵 100 元，线下收 85.00，当场换过去（offline 一行收款、PaidHook 一次、审计方向 upgrade）。
// 最后：同一张凭证号再开一单回「凭证号已用于其他订单」（放最后：出错时已写的部分留在外层事务里）。
func TestManualOrderOfflineChange(t *testing.T) {
	e, ctx := newEnv(t)
	hook := &spyHook{}
	e.svc.UsePaidHook(hook)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month", nil, nil)

	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planB, PriceID: priceB, Reason: manualReason,
		Settlement: SettlementOffline, Reference: "BANK-DUP-1", Target: &Choice{Kind: PlacementChange, SubscriptionID: &sub.ID},
	})
	if err != nil {
		t.Fatalf("线下已收款换套餐: %v", err)
	}
	if c.Status != StatusFulfilled || c.Change == nil {
		t.Fatalf("结果 %+v", c)
	}
	mustEq(t, "Payable", c.Payable, dec("85"))
	mustEq(t, "ProrationCredit", c.Change.ProrationCredit, dec("15"))
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s", p)
	}
	if n := e.count(t, ctx, `
SELECT count(*) FROM payments p JOIN payment_providers pp ON pp.id = p.provider_id
WHERE p.order_id = ? AND pp.code = 'offline'`, c.OrderID); n != 1 {
		t.Fatalf("offline 收款 %d 行", n)
	}
	if calls := hook.snapshot(); len(calls) != 1 || calls[0].Kind != KindUpgrade || calls[0].OrderID != c.OrderID {
		t.Fatalf("PaidHook %+v", calls)
	}
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 0 {
		t.Fatalf("升级不该退余额，plan_change_refund %d 笔", n)
	}
	if v := e.auditField(t, ctx, ActionManualCreated, c.OrderID, "direction"); v != "upgrade" {
		t.Fatalf("审计 direction=%q", v)
	}
	e.assertNoDrift(t, ctx)

	other := e.mkUser(t, ctx)
	planN, _, priceN := e.mkPlan(t, ctx, 2000, "month", nil, nil)
	_, err = e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: other, PlanID: planN, PriceID: priceN, Reason: manualReason,
		Settlement: SettlementOffline, Reference: "BANK-DUP-1",
	})
	mustErrMsg(t, "凭证号重复", err, paymentsvc.ErrPaymentReused)
}

// 预览与建单同口径（审查 S7、S3）：目标套餐不允许变更时 change 标不可选、默认值不落在它上面；价格币种与原订阅本期不同标不可选；
// 隐藏套餐「另开一份」标不可选、建单 404（换套餐不看可见性）；这一份挂着未完结的续费或变更单时标不可选。
func TestManualPreviewMatchesCreate(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	changeKey := placementKey(PlacementChange, &sub.ID)
	optionOf := func(pv ManualPreview, key string) Placement {
		t.Helper()
		for _, op := range pv.Options {
			if op.Key == key {
				return op
			}
		}
		t.Fatalf("预览里没有 %s：%+v", key, pv.Options)
		return Placement{}
	}
	preview := func(planID, priceID uuid.UUID) ManualPreview {
		t.Helper()
		pv, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planID, PriceID: &priceID, EntrySubscriptionID: &sub.ID})
		if err != nil {
			t.Fatalf("预览: %v", err)
		}
		return pv
	}

	locked, _, lockedPrice := e.mkPlan(t, ctx, 10000, "month", nil, nil)
	e.mustExec(t, ctx, `UPDATE plans SET allow_upgrade = false WHERE id = ?`, locked)
	pv := preview(locked, lockedPrice)
	if op := optionOf(pv, changeKey); op.Unavailable != catalogsvc.ErrChangeNotAllowed.Message {
		t.Fatalf("不允许变更：change 选项 %q", op.Unavailable)
	}
	if op := optionOf(pv, PlacementNew); op.Unavailable != "" {
		t.Fatalf("另开一份应可选：%q", op.Unavailable)
	}
	if pv.DefaultKey != "" {
		t.Fatalf("默认值不该落在开不了的项上：%q", pv.DefaultKey)
	}
	_, err := e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: locked, PriceID: lockedPrice, Reason: manualReason,
		Target: &Choice{Kind: PlacementChange, SubscriptionID: &sub.ID}})
	mustErrMsg(t, "建单：不允许变更", err, catalogsvc.ErrChangeNotAllowed)

	planUSD, _, _ := e.mkPlan(t, ctx, 1000, "month", nil, nil)
	e.mustExec(t, ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE plan_id = ?`, planUSD)
	var usdPrice uuid.UUID
	if err := e.row(ctx, `INSERT INTO prices (plan_id, currency, unit_amount, billing_interval, interval_count) VALUES (?, 'USD', 10, 'month', 1) RETURNING id`,
		planUSD).Scan(&usdPrice); err != nil {
		t.Fatal(err)
	}
	pv = preview(planUSD, usdPrice)
	op := optionOf(pv, changeKey)
	if op.Unavailable != ErrPlanChangeCurrency.Message {
		t.Fatalf("币种不一致：change 选项 %q", op.Unavailable)
	}
	mustEq(t, "币种不一致时应付按全价", op.Due, dec("10"))

	hidden, _, hiddenPrice := e.mkPlan(t, ctx, 10000, "month", nil, nil)
	e.mustExec(t, ctx, `UPDATE plans SET visibility = 'hidden' WHERE id = ?`, hidden)
	pv = preview(hidden, hiddenPrice)
	if op := optionOf(pv, PlacementNew); op.Unavailable == "" {
		t.Fatal("隐藏套餐另开一份应标不可选")
	}
	if op := optionOf(pv, changeKey); op.Unavailable != "" {
		t.Fatalf("换成隐藏套餐不看可见性，应可选：%q", op.Unavailable)
	}
	_, err = e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: hidden, PriceID: hiddenPrice, Reason: manualReason,
		Target: &Choice{Kind: PlacementNew}})
	if codeOf(err) != errs.NotFound {
		t.Fatalf("隐藏套餐另开一份应 404，得到 %v", err)
	}

	// 开一张待付的人工换套餐单：这一份挂着未完结单，之后的预览把它标不可选
	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: hidden, PriceID: hiddenPrice, Reason: manualReason,
		Settlement: SettlementPending, Target: &Choice{Kind: PlacementChange, SubscriptionID: &sub.ID}})
	if err != nil || c.Status != StatusPending {
		t.Fatalf("待付人工换套餐: %+v %v", c, err)
	}
	planD, _, priceD := e.mkPlan(t, ctx, 5000, "month", nil, nil)
	if op := optionOf(preview(planD, priceD), changeKey); op.Unavailable != ErrOpenChange.Message {
		t.Fatalf("有未完结单：change 选项 %q", op.Unavailable)
	}
}

// 幂等重放：门户换套餐（subscription_change_plan_create）与后台人工单（order_create，认领属于管理员）建单与幂等完成同一事务，
// 同一个键再来拿到首发的响应，不再建单。
func TestPlanChangeAndManualIdempotentReplay(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month", nil, nil)
	reply := func(c Created) (idemsvc.Response, error) {
		return idemsvc.JSON(201, map[string]string{"order_id": c.OrderID.String()})
	}
	run := func(actx context.Context, actor uuid.UUID, scope string, create func(context.Context) (Created, error)) {
		t.Helper()
		key := "idem-" + randHex(16)
		fp := idemsvc.NewFingerprint("POST", "/api/v1/"+scope, []byte(`{}`))
		claim, replay, err := e.idem.Claim(actx, scope, key, fp)
		if err != nil || claim == nil || replay != nil {
			t.Fatalf("%s 认领: claim=%v replay=%v err=%v", scope, claim, replay, err)
		}
		c, err := create(idemsvc.WithClaim(actx, claim))
		if err != nil {
			t.Fatalf("%s 建单: %v", scope, err)
		}
		var resID uuid.UUID
		var code int
		if err := e.row(ctx, `SELECT resource_id, response_code FROM idempotency_keys WHERE scope = ? AND actor_id = ? AND idempotency_key = ?`,
			scope, actor, key).Scan(&resID, &code); err != nil {
			t.Fatal(err)
		}
		if resID != c.OrderID || code != 201 {
			t.Fatalf("%s 幂等记录 resource=%s code=%d", scope, resID, code)
		}
		before := e.count(t, ctx, `SELECT count(*) FROM orders`)
		_, replay, err = e.idem.Claim(actx, scope, key, fp)
		if err != nil || replay == nil || replay.Status != 201 || !strings.Contains(string(replay.Body), c.OrderID.String()) {
			t.Fatalf("%s 重放: %+v %v", scope, replay, err)
		}
		if after := e.count(t, ctx, `SELECT count(*) FROM orders`); after != before {
			t.Fatalf("%s 重放又建了单", scope)
		}
	}
	run(asUser(ctx, user), user, "subscription_change_plan_create", func(c context.Context) (Created, error) {
		return e.svc.CreatePlanChangeOrder(c, PlanChangeOrderInput{SubscriptionID: sub.ID, PlanID: planB, PriceID: priceB, Reply: reply})
	})
	planN, _, priceN := e.mkPlan(t, ctx, 2000, "month", nil, nil)
	other := e.mkUser(t, ctx)
	run(asAdmin(ctx, admin), admin, "order_create", func(c context.Context) (Created, error) {
		return e.svc.CreateManualOrder(c, ManualOrderInput{UserID: other, PlanID: planN, PriceID: priceN, Reason: manualReason, Reply: reply})
	})
}
