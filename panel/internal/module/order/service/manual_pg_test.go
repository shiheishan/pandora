package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const manualReason = "客户线下转账购买，已核对流水"

// 人工单建单加线下收款：没有订阅时只有「另开一份」；线下已收款当场结清（offline 渠道一行收款、审计一条），
// 订单记下开单人与原因；之后同款只有「续一期」，赠送续费全额减免、当场把到期日往后推一期。
func TestManualOrderOfflineNewThenGrantRenewal(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")

	pv, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planID, PriceID: &priceID})
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	if len(pv.Options) != 1 || pv.Options[0].Kind != PlacementNew || pv.DefaultKey != PlacementNew || pv.Options[0].NewPeriodEnd == nil {
		t.Fatalf("预览 %+v", pv)
	}
	mustEq(t, "预览应付", pv.Options[0].Due, dec("20"))

	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planID, PriceID: priceID,
		Reason: "  " + manualReason + "  ", Settlement: SettlementOffline, Reference: " BANK-77 ",
	})
	if err != nil {
		t.Fatalf("线下已收款开单: %v", err)
	}
	if c.Status != StatusFulfilled {
		t.Fatalf("状态 %s，期望当场履约", c.Status)
	}
	mustEq(t, "Payable", c.Payable, dec("20"))
	o := e.order(t, ctx, c.OrderID)
	if o.Kind != KindNew || o.CreatedBy == nil || *o.CreatedBy != admin || o.ManualReason == nil || *o.ManualReason != manualReason {
		t.Fatalf("订单 kind=%s created_by=%v reason=%v", o.Kind, o.CreatedBy, o.ManualReason)
	}
	mustEq(t, "paid_amount", o.PaidAmount, dec("20"))
	mustEq(t, "balance_applied", o.BalanceApplied, dec("0"))
	if o.SubscriptionID == nil {
		t.Fatal("没有开出订阅")
	}
	if n := e.count(t, ctx, `
SELECT count(*) FROM payments p JOIN payment_providers pp ON pp.id = p.provider_id
WHERE p.order_id = ? AND pp.code = 'offline'`, c.OrderID); n != 1 {
		t.Fatalf("offline 收款 %d 行", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionManualCreated, c.OrderID); n != 1 {
		t.Fatalf("人工单审计 %d 条", n)
	}

	// 有了同款的一份：只剩「续一期」，不问直接续
	pv, err = e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planID, PriceID: &priceID})
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	if len(pv.Options) != 1 || pv.Options[0].Kind != PlacementRenew || pv.DefaultKey != pv.Options[0].Key {
		t.Fatalf("预览 %+v", pv)
	}
	before, err := e.subs.Get(ctx, *o.SubscriptionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: planID, PriceID: priceID, Reason: "故障补偿，赠送一个月"})
	if err != nil {
		t.Fatalf("赠送续费: %v", err)
	}
	if g.Status != StatusFulfilled {
		t.Fatalf("赠送状态 %s", g.Status)
	}
	mustEq(t, "赠送 Discount", g.Discount, dec("20"))
	mustEq(t, "赠送 Total", g.Total, dec("0"))
	if gord := e.order(t, ctx, g.OrderID); gord.Kind != KindRenewal || gord.SubscriptionID == nil || *gord.SubscriptionID != before.ID {
		t.Fatalf("赠送单 kind=%s sub=%v", gord.Kind, gord.SubscriptionID)
	}
	after, err := e.subs.Get(ctx, before.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := subsvc.AddInterval(before.PeriodEnd, "month", 1); !after.PeriodEnd.Equal(want) {
		t.Fatalf("到期日 %s，期望 %s", after.PeriodEnd, want)
	}
	e.assertNoDrift(t, ctx)
}

// 人工赠送换套餐：新价算 0 元，原套餐付费时长用掉一半、剩余价值 15.00 全额退进余额；线下已收款而应付为 0 的单回 409「请改用赠送」。
func TestManualOrderChange(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	sub := e.activeSub(t, ctx, user, 3000, 50)
	e.usePaidTime(t, ctx, sub.ID, 1, 2)
	planB, _, priceB := e.mkPlan(t, ctx, 10000, "month")
	planC, _, priceC := e.mkPlan(t, ctx, 1000, "month")
	target := &Choice{Kind: PlacementChange, SubscriptionID: &sub.ID}

	pv, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: planB, PriceID: &priceB, EntrySubscriptionID: &sub.ID})
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	if len(pv.Options) != 2 || pv.Options[0].Kind != PlacementNew || pv.Options[1].Kind != PlacementChange ||
		pv.DefaultKey != pv.Options[1].Key {
		t.Fatalf("预览 %+v", pv)
	}
	ch := pv.Options[1]
	mustEq(t, "预览剩余价值", ch.Credit, dec("15"))
	mustEq(t, "预览应付", ch.Due, dec("85"))

	// 两个选项而没选：422 落在 target 上
	_, err = e.svc.CreateManualOrder(actx, ManualOrderInput{UserID: user, PlanID: planB, PriceID: priceB, Reason: manualReason})
	if errs.As(err).Fields["target"] == "" {
		t.Fatalf("多个选项没选应 422 target，得到 %v", err)
	}
	c, err := e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: user, PlanID: planB, PriceID: priceB, Reason: manualReason, Target: target,
	})
	if err != nil {
		t.Fatalf("赠送换套餐: %v", err)
	}
	if c.Status != StatusFulfilled || c.Change == nil {
		t.Fatalf("结果 %+v", c)
	}
	mustEq(t, "Discount", c.Discount, dec("100"))
	mustEq(t, "Total", c.Total, dec("0"))
	mustEq(t, "BalanceRefund", c.Change.BalanceRefund, dec("15"))
	if p := e.planOf(t, ctx, sub.ID); p != planB {
		t.Fatalf("订阅套餐 %s，期望 %s", p, planB)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("15"))
	if n := e.txnCount(t, ctx, ledgersvc.KindPlanChangeRefund, c.OrderID); n != 1 {
		t.Fatalf("plan_change_refund %d 笔", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, ActionManualCreated, c.OrderID); n != 1 {
		t.Fatalf("人工单审计 %d 条", n)
	}
	e.assertNoDrift(t, ctx)

	// 另一个用户：刚买下 30 元/月（时钟定在上面那一刻，至多晚开通 1.5 天），剩余价值远高于新价 10 元，线下已收款没东西可收，建单前就拒掉、不落订单
	other := e.mkUser(t, ctx)
	otherSub := e.activeSub(t, ctx, other, 3000, 50)
	_, err = e.svc.CreateManualOrder(actx, ManualOrderInput{
		UserID: other, PlanID: planC, PriceID: priceC, Reason: manualReason,
		Settlement: SettlementOffline, Reference: "BANK-1", Target: &Choice{Kind: PlacementChange, SubscriptionID: &otherSub.ID},
	})
	mustErrMsg(t, "应付为 0 的线下收款", err, errOfflineChangeNothingDue)
	// activeSub 自己买下原套餐落过一张新购单，只数换套餐单
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE user_id = ? AND kind = 'upgrade'`, other); n != 0 {
		t.Fatalf("应付为 0 的线下收款不该落订单，有 %d 张", n)
	}
}

// 人工单原因与结算方式校验：去掉首尾空白后不足 5 个字、超过 500 字回 422；balance 与未知结算方式 422；
// offline 不带凭证号 422；门户主体 403；用户不存在 404。库里的 CHECK 兜住绕过 service 的写入。
func TestManualOrderValidation(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	admin := e.mkUser(t, ctx)
	actx := asAdmin(ctx, admin)
	planID, _, priceID := e.mkPlan(t, ctx, 2000, "month")
	in := func(reason, settlement, ref string) ManualOrderInput {
		return ManualOrderInput{UserID: user, PlanID: planID, PriceID: priceID, Reason: reason, Settlement: settlement, Reference: ref}
	}
	cases := []struct {
		name  string
		in    ManualOrderInput
		field string
	}{
		{"两个字", in("补偿", "", ""), "reason"},
		{"去掉空白只剩四个字", in("   补偿一下   ", "", ""), "reason"},
		{"超过 500 字", in(strings.Repeat("字", 501), "", ""), "reason"},
		{"从余额扣", in(manualReason, "balance", ""), "settlement"},
		{"未知结算方式", in(manualReason, "credit", ""), "settlement"},
		{"线下不带凭证号", in(manualReason, SettlementOffline, "   "), "reference"},
		{"凭证号超过 128 字", in(manualReason, SettlementOffline, strings.Repeat("A", 129)), "reference"},
	}
	for _, tc := range cases {
		_, err := e.svc.CreateManualOrder(actx, tc.in)
		if codeOf(err) != errs.ValidationFailed || errs.As(err).Fields[tc.field] == "" {
			t.Fatalf("%s：应 422 落在 %s，得到 %v", tc.name, tc.field, err)
		}
	}
	if _, err := e.svc.CreateManualOrder(asUser(ctx, admin), in(manualReason, "", "")); codeOf(err) != errs.Forbidden {
		t.Fatalf("门户主体应 403，得到 %v", err)
	}
	ghost := in(manualReason, "", "")
	ghost.UserID = uuid.New()
	if _, err := e.svc.CreateManualOrder(actx, ghost); codeOf(err) != errs.NotFound {
		t.Fatalf("用户不存在应 404，得到 %v", err)
	}
	if _, err := e.svc.ManualPreview(actx, ManualPreviewInput{UserID: user, PlanID: uuid.New()}); codeOf(err) != errs.NotFound {
		t.Fatalf("套餐不存在应 404，得到 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM orders WHERE created_by = ?`, admin); n != 0 {
		t.Fatalf("校验失败不该留下订单，有 %d 张", n)
	}

	// 恰好 5 个字可以
	c, err := e.svc.CreateManualOrder(actx, in("故障补偿！", "", ""))
	if err != nil {
		t.Fatalf("5 个字的原因: %v", err)
	}
	// 库里的 CHECK：开单人与原因要么都有、要么都没有，原因去掉空白后至少 5 个字符（放在最后：违反约束会中止外层事务）
	if err := e.exec(ctx, `UPDATE orders SET manual_reason = '  ab  ' WHERE id = ?`, c.OrderID); err == nil {
		t.Fatal("原因不足 5 个字符应被 CHECK 拒绝")
	}
}
