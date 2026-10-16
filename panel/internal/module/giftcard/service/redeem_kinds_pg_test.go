package service

import (
	"errors"
	"strings"
	"testing"
)

// 同套餐的一次性套餐卡：默认叠加，也能新开；叠加后总流量增加，到期日顺延，流水记 renewed。
func TestRedeemPlanCardStacksOneTime(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	days30 := 30
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 3000, &days30)
	sub := e.mkOneTimeSub(t, ctx, user, planID, versionID, priceID, &days30)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	pv, err := e.svc.Preview(asUser(ctx, user), code)
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	if pv.Placement == nil {
		t.Fatal("预览缺 Placement")
	}
	var hasStack, hasNew bool
	for _, op := range pv.Placement.Options {
		if op.Kind == KindStack && op.SubscriptionID != nil && *op.SubscriptionID == sub.ID {
			hasStack = true
		}
		if op.Kind == KindNew {
			hasNew = true
		}
	}
	if !hasStack || !hasNew {
		t.Fatalf("预览选项缺少叠加或新开: %+v", pv.Placement.Options)
	}
	if pv.Placement.DefaultKey != optionKey(KindStack, sub.ID) {
		t.Fatalf("默认落点 %s，期望叠加 %s", pv.Placement.DefaultKey, optionKey(KindStack, sub.ID))
	}

	choice := &Choice{Kind: KindStack, SubscriptionID: &sub.ID}
	res, err := e.redeem(ctx, user, code, choice)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}
	if !strings.Contains(res.PlanGranted, "一次性套餐") {
		t.Fatalf("回执套餐名 %q", res.PlanGranted)
	}

	var count int64
	if err := e.row(ctx, `SELECT count(*) FROM gift_card_redemptions WHERE plan_mode = 'renewed' AND subscription_id = ?`, sub.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("流水 renewed 笔数 %d (%v)，期望 1", count, err)
	}

	var limitVal int64
	if err := e.row(ctx, `SELECT limit_value FROM quota_balances WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'total'`, sub.ID).Scan(&limitVal); err != nil || limitVal != 200*gib {
		t.Fatalf("叠加后 limit_value = %d (%v)，期望 %d", limitVal, err, 200*gib)
	}

	after := e.subOf(t, ctx, sub.ID)
	if !after.PeriodEnd.After(sub.PeriodEnd) {
		t.Fatalf("期末 %s 未晚于原期末 %s", after.PeriodEnd, sub.PeriodEnd)
	}
}

// 同套餐的一次性套餐卡：选「另开一份」后名下有两份订阅。
func TestRedeemPlanCardNewOnOneTimeSamePlan(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	days30 := 30
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 3000, &days30)
	e.mkOneTimeSub(t, ctx, user, planID, versionID, priceID, &days30)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	choice := &Choice{Kind: KindNew}
	if _, err := e.redeem(ctx, user, code, choice); err != nil {
		t.Fatalf("新开兑换: %v", err)
	}

	var subCount int64
	if err := e.row(ctx, `SELECT count(*) FROM subscriptions WHERE user_id = ?`, user).Scan(&subCount); err != nil || subCount != 2 {
		t.Fatalf("订阅总数 %d (%v)，期望 2", subCount, err)
	}
}

// 续费（KindRenew）时套餐已归档：回 ErrRenewArchived，码仍 unused。
func TestRenewRejectedAfterPlanArchived(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, planID, versionID, priceID)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	e.mustExec(t, ctx, `UPDATE plans SET status = 'archived' WHERE id = ?`, planID)

	choice := &Choice{Kind: KindRenew, SubscriptionID: &sub.ID}
	_, err := e.redeem(ctx, user, code, choice)
	if !errors.Is(err, ErrRenewArchived) {
		t.Fatalf("归档续费应回 ErrRenewArchived，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, code); st != CodeUnused {
		t.Fatalf("码状态 %s，期望 unused", st)
	}
}

// 套餐卡的价格已归档：拒绝兑换（提示价格已下架），码仍 unused。
func TestRedeemRejectsArchivedPrice(t *testing.T) {
	e, ctx := newEnv(t)
	planID, _, priceID := e.mkPlan(t, ctx, 3000)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID, PriceID: &priceID}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	e.mustExec(t, ctx, `UPDATE prices SET status = 'archived', row_version = row_version + 1 WHERE id = ?`, priceID)

	otherUser := e.mkUser(t, ctx)
	_, err := e.redeem(ctx, otherUser, code, nil)
	if err == nil || !strings.Contains(err.Error(), "价格已下架") {
		t.Fatalf("应回价格已下架错误，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, code); st != CodeUnused {
		t.Fatalf("码状态 %s，期望 unused", st)
	}
}

// 盲盒抽中加时长落到永久一次性订阅：跳过加时长，回执与流水体现，其余奖励（余额）照发，期末不变。
func TestMysteryDaysSkippedOnPermanent(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 3000, nil)
	sub := e.mkOneTimeSub(t, ctx, user, planID, versionID, priceID, nil)

	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeMystery, Rewards: Rewards{Pool: []Prize{
		{Label: "特奖甲", Weight: 1, ExpireDays: 7, Balance: dec("1")},
		{Label: "特奖乙", Weight: 1, ExpireDays: 7, Balance: dec("1")},
	}}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	res, err := e.redeem(ctx, user, code, nil)
	if err != nil {
		t.Fatalf("兑换: %v", err)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("1"))

	var skippedSummary bool
	for _, s := range res.Summary {
		if strings.Contains(s, "已跳过") {
			skippedSummary = true
		}
	}
	if !skippedSummary {
		t.Fatalf("回执 Summary 缺少「已跳过」: %+v", res.Summary)
	}

	var hasExpireDays bool
	if err := e.row(ctx, `SELECT expire_days IS NOT NULL FROM gift_card_redemptions WHERE code_id = ?`, e.codeID(t, ctx, code)).Scan(&hasExpireDays); err != nil {
		t.Fatal(err)
	}
	if hasExpireDays {
		t.Fatal("兑换流水 expire_days 期望为 NULL")
	}

	after := e.subOf(t, ctx, sub.ID)
	if !after.PeriodEnd.Equal(sub.PeriodEnd) {
		t.Fatalf("永久套餐期末变动: 前 %s 后 %s", sub.PeriodEnd, after.PeriodEnd)
	}
}

// 通用加时长卡：用户只有永久一次性订阅时无法加时长，回 ErrNoPlacement，码仍 unused。
func TestGeneralDaysCardSkipsPermanentSub(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkOneTimePlan(t, ctx, 3000, nil)
	e.mkOneTimeSub(t, ctx, user, planID, versionID, priceID, nil)

	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{ExpireDays: 7}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]

	_, err := e.redeem(ctx, user, code, nil)
	if !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("期望 ErrNoPlacement，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, code); st != CodeUnused {
		t.Fatalf("码状态 %s，期望 unused", st)
	}
}
