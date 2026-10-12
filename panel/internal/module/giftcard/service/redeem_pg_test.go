package service

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 余额卡：余额入账一笔 gift_card_balance（平台赠送费用 → 用户余额），码转 used，写一条兑换流水；
// 同一张码再兑回同一句「不能用」；兑换流水只追加。
func TestRedeemBalanceCard(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("10.50")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	res, err := e.redeem(ctx, user, " "+code+" ", nil)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}
	mustEq(t, "回执余额", res.Balance, dec("10.50"))
	if len(res.Summary) == 0 {
		t.Fatal("回执 summary 为空")
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("10.50"))
	if st := e.codeStatus(t, ctx, code); st != CodeUsed {
		t.Fatalf("码状态 %s，期望 used", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'gift_card_balance' AND source_type = 'gift_card_code'`); n != 1 {
		t.Fatalf("赠送交易 %d 笔，期望 1", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_redemptions WHERE user_id = ? AND ledger_txn_id IS NOT NULL`, user); n != 1 {
		t.Fatalf("兑换流水 %d 条，期望 1", n)
	}

	_, err = e.redeem(ctx, user, code, nil)
	if !errors.Is(err, ErrCodeUnusable) {
		t.Fatalf("重复兑换应回 ErrCodeUnusable，得到 %v", err)
	}
	mustEq(t, "重复兑换后余额", e.balanceOf(t, ctx, user), dec("10.50"))

	if err := e.exec(ctx, `UPDATE gift_card_redemptions SET user_id = user_id`); err == nil {
		t.Fatal("兑换流水应只追加")
	}
}

// 不存在、格式不对、停用、过期的码一律回同一个错误；过期的码顺手改成 expired。
func TestRedeemUnusableCodesLookTheSame(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	codes := e.mkCodes(t, ctx, tpl.ID, 3, nil)

	for _, c := range []string{"NOSUCHCODE123", "bad", "含中文的卡密12345"} {
		if _, err := e.redeem(ctx, user, c, nil); !errors.Is(err, ErrCodeUnusable) {
			t.Fatalf("码 %q 应回 ErrCodeUnusable，得到 %v", c, err)
		}
	}

	var disabledID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM gift_card_codes WHERE code_hash = ?`, e.svc.hashCode(codes[0])).Scan(&disabledID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ToggleCode(e.asAdmin(ctx), disabledID, true); err != nil {
		t.Fatalf("停用: %v", err)
	}
	if _, err := e.redeem(ctx, user, codes[0], nil); !errors.Is(err, ErrCodeUnusable) {
		t.Fatalf("停用的码应回 ErrCodeUnusable，得到 %v", err)
	}

	e.mustExec(t, ctx, `UPDATE gift_card_codes SET expires_at = now() - interval '1 minute' WHERE code_hash = ?`, e.svc.hashCode(codes[1]))
	if _, err := e.redeem(ctx, user, codes[1], nil); !errors.Is(err, ErrCodeUnusable) {
		t.Fatalf("过期的码应回 ErrCodeUnusable，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, codes[1]); st != CodeExpired {
		t.Fatalf("过期的码状态 %s，期望 expired", st)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), decimal.Zero)

	// 已兑换的码不能停用
	if _, err := e.redeem(ctx, user, codes[2], nil); err != nil {
		t.Fatal(err)
	}
	var usedID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM gift_card_codes WHERE code_hash = ?`, e.svc.hashCode(codes[2])).Scan(&usedID); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.svc.ToggleCode(e.asAdmin(ctx), usedID, true), errs.Conflict)
}

// 套餐卡、无订阅：另开一份；没有价格档时快照按 month×1、金额 0。
func TestRedeemPlanCardProvisionsNew(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, _, _ := e.mkPlan(t, ctx, 3000)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	pv, err := e.svc.Preview(asUser(ctx, user), code)
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	if pv.Placement == nil || len(pv.Placement.Options) != 1 || pv.Placement.Options[0].Kind != KindNew || pv.Placement.DefaultKey != KindNew {
		t.Fatalf("预览落点 %+v，期望只有「另开一份」并默认选中", pv.Placement)
	}

	res, err := e.redeem(ctx, user, code, nil)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}
	if res.PlanGranted == "" {
		t.Fatal("回执缺 plan_granted")
	}
	us, err := e.subs.ByUser(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(us.Subscriptions) != 1 {
		t.Fatalf("订阅 %d 份，期望 1", len(us.Subscriptions))
	}
	s := us.Subscriptions[0]
	if s.PlanID != planID || s.Price.Interval != "month" || s.Price.IntervalCount != 1 || !s.Price.Amount.IsZero() || s.Price.PriceID != nil {
		t.Fatalf("订阅快照 %+v，期望 month×1、金额 0、无价格档", s.Price)
	}
	var mode string
	if err := e.row(ctx, `SELECT plan_mode FROM gift_card_redemptions WHERE user_id = ?`, user).Scan(&mode); err != nil || mode != PlanGrantNew {
		t.Fatalf("plan_mode %q (%v)，期望 new", mode, err)
	}
}

// 套餐卡、有同款：续那一份，到期日接着原到期日推一期；快照金额记 0。
func TestRedeemPlanCardRenewsSamePlan(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, planID, versionID, priceID)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	if _, err := e.redeem(ctx, user, code, nil); err != nil {
		t.Fatalf("兑换: %v", err)
	}
	after := e.subOf(t, ctx, sub.ID)
	want := subsvc.AddInterval(sub.PeriodEnd.UTC(), "month", 1)
	if !after.PeriodEnd.Equal(want) {
		t.Fatalf("到期 %s，期望 %s", after.PeriodEnd, want)
	}
	if !after.Price.Amount.IsZero() {
		t.Fatalf("续期快照金额 %s，期望 0", after.Price.Amount)
	}
}

// 套餐卡换掉不同款：没装折算时不给「换掉」；装上后换掉并把剩余价值退进余额（plan_change_refund）。
func TestRedeemPlanCardChangesPlan(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	oldPlan, oldVersion, oldPrice := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, oldPlan, oldVersion, oldPrice)
	newPlan, _, _ := e.mkPlan(t, ctx, 5000)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &newPlan}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)
	change := &Choice{Kind: KindChange, SubscriptionID: &sub.ID}

	if _, err := e.redeem(ctx, user, codes[0], change); !errors.Is(err, ErrChoiceStale) {
		t.Fatalf("没装折算时换掉应回 ErrChoiceStale，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, codes[0]); st != CodeUnused {
		t.Fatalf("失败的兑换不该用掉码，状态 %s", st)
	}

	e.svc.UsePlanChangeValuer(fakeValuer{value: dec("12.34"), lost: 2})
	pv, err := e.svc.Preview(asUser(ctx, user), codes[0])
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, o := range pv.Placement.Options {
		if o.Kind == KindChange && o.SubscriptionID != nil && *o.SubscriptionID == sub.ID {
			found = true
			mustEq(t, "预览退款", o.Credit, dec("12.34"))
		}
	}
	if !found {
		t.Fatalf("预览里没有「换掉」选项: %+v", pv.Placement.Options)
	}

	if _, err := e.redeem(ctx, user, codes[0], change); err != nil {
		t.Fatalf("换套餐兑换: %v", err)
	}
	if after := e.subOf(t, ctx, sub.ID); after.PlanID != newPlan {
		t.Fatalf("订阅套餐 %s，期望换成 %s", after.PlanID, newPlan)
	}
	mustEq(t, "退回余额", e.balanceOf(t, ctx, user), dec("12.34"))
	if n := e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'plan_change_refund' AND source_type = 'gift_card_code'`); n != 1 {
		t.Fatalf("退款交易 %d 笔，期望 1", n)
	}
	var refund decimal.Decimal
	if err := e.row(ctx, `SELECT refund_amount FROM gift_card_redemptions WHERE plan_mode = 'changed' AND user_id = ?`, user).Scan(&refund); err != nil {
		t.Fatal(err)
	}
	mustEq(t, "流水退款", refund, dec("12.34"))
}

// 加时长：生效中的往后推；已过期 30 天内的救回。
func TestRedeemExtendDaysAndRescue(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, planID, versionID, priceID)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{ExpireDays: 7}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)

	res, err := e.redeem(ctx, user, codes[0], nil)
	if err != nil {
		t.Fatalf("加时长: %v", err)
	}
	if res.ExpireDays != 7 {
		t.Fatalf("回执天数 %d", res.ExpireDays)
	}
	after := e.subOf(t, ctx, sub.ID)
	if want := sub.PeriodEnd.AddDate(0, 0, 7); !after.PeriodEnd.Equal(want) {
		t.Fatalf("到期 %s，期望 %s", after.PeriodEnd, want)
	}

	e.mustExec(t, ctx, `UPDATE subscriptions SET status = 'expired', current_period_start = now() - interval '33 days',
		current_period_end = now() - interval '3 days' WHERE id = ?`, sub.ID)
	if _, err := e.redeem(ctx, user, codes[1], nil); err != nil {
		t.Fatalf("救回: %v", err)
	}
	rescued := e.subOf(t, ctx, sub.ID)
	if rescued.Status != subsvc.StatusActive || !rescued.PeriodEnd.After(time.Now().AddDate(0, 0, 6)) {
		t.Fatalf("救回后 status=%s end=%s", rescued.Status, rescued.PeriodEnd)
	}
}

// 送流量：有生效订阅时挂上去；一份都没有时留作未分配，照样能兑。
func TestRedeemTrafficPack(t *testing.T) {
	e, ctx := newEnv(t)
	withSub := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, withSub, planID, versionID, priceID)
	noSub := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{TrafficBytes: 5 * gib}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)

	if _, err := e.redeem(ctx, withSub, codes[0], nil); err != nil {
		t.Fatalf("有订阅兑流量: %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM traffic_pack_grants WHERE user_id = ? AND subscription_id = ? AND source = 'gift_card' AND granted_bytes = ?`,
		withSub, sub.ID, 5*gib); n != 1 {
		t.Fatalf("挂到订阅上的流量包 %d 笔，期望 1", n)
	}
	if _, err := e.redeem(ctx, noSub, codes[1], nil); err != nil {
		t.Fatalf("无订阅兑流量: %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM traffic_pack_grants WHERE user_id = ? AND subscription_id IS NULL`, noSub); n != 1 {
		t.Fatalf("未分配的流量包 %d 笔，期望 1", n)
	}
}

// 流量重置：本期已用清零；没有生效订阅时回「没有在用的套餐」，码不被用掉。
func TestRedeemResetTraffic(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{ResetQuota: true}})
	codes := e.mkCodes(t, ctx, tpl.ID, 1, nil)

	if _, err := e.redeem(ctx, user, codes[0], nil); !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("无订阅时应回 ErrNoPlacement，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, codes[0]); st != CodeUnused {
		t.Fatalf("码状态 %s，期望仍是 unused", st)
	}

	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, planID, versionID, priceID)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes'`, 40*gib, sub.ID)
	res, err := e.redeem(ctx, user, codes[0], nil)
	if err != nil {
		t.Fatalf("重置: %v", err)
	}
	if !res.QuotaReset {
		t.Fatal("回执缺 quota_reset")
	}
	if n := e.count(t, ctx, `SELECT coalesce(sum(consumed), 0) FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes'`, sub.ID); n != 0 {
		t.Fatalf("重置后已用 %d，期望 0", n)
	}
}

// 盲盒：按奖池发；预览只回奖品名，不回权重与奖励。
func TestRedeemMysteryCard(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{
		{Label: "小奖", Weight: 7, Balance: dec("1")},
		{Label: "大奖", Weight: 3, Balance: dec("5")},
	}}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	pv, err := e.svc.Preview(asUser(ctx, user), code)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pv.Rewards.Pool {
		if p.Weight != 0 || !p.Balance.IsZero() {
			t.Fatalf("预览泄露了奖池权重或奖励: %+v", p)
		}
	}
	res, err := e.redeem(ctx, user, code, nil)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}
	switch res.PrizeLabel {
	case "小奖":
		mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("1"))
	case "大奖":
		mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("5"))
	default:
		t.Fatalf("抽中 %q，不在奖池里", res.PrizeLabel)
	}
}

// 领取条件与每人限次：仅新用户在没装付费记录时宁可不让兑（503）；每人限 1 次时第二张同模板的码被拒。
func TestRedeemConditionsAndLimits(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	newOnly := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")},
		Conditions: Conditions{NewUserOnly: true}})
	nc := e.mkCodes(t, ctx, newOnly.ID, 2, nil)
	_, err := e.redeem(ctx, user, nc[0], nil)
	mustCode(t, err, errs.Unavailable)

	paidUser := e.mkUser(t, ctx)
	e.svc.UsePaidHistory(fakePaid{paid: map[uuid.UUID]bool{paidUser: true}})
	_, err = e.redeem(ctx, paidUser, nc[0], nil)
	mustCode(t, err, errs.ValidationFailed)
	if _, err := e.redeem(ctx, user, nc[0], nil); err != nil {
		t.Fatalf("新用户兑换: %v", err)
	}

	once := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")},
		Limits: Limits{MaxUsePerUser: 1}})
	oc := e.mkCodes(t, ctx, once.ID, 2, nil)
	if _, err := e.redeem(ctx, user, oc[0], nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.redeem(ctx, user, oc[1], nil)
	mustCode(t, err, errs.ValidationFailed)
	if st := e.codeStatus(t, ctx, oc[1]); st != CodeUnused {
		t.Fatalf("被限次拒绝的码状态 %s，期望 unused", st)
	}
}

// 我的兑换记录只给掩码。
func TestMyRedemptionsMasked(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("2")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]
	if _, err := e.redeem(ctx, user, code, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.MyRedemptions(asUser(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CodeHint != MaskCode(code) || rows[0].CodeHint == code {
		t.Fatalf("我的兑换记录 %+v", rows)
	}
	mustEq(t, "记录余额", rows[0].Balance, dec("2"))
}
