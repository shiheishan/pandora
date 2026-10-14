package service

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 码已转 used、之后发放失败：整笔回滚，码仍是 unused，没有兑换流水，余额那笔账也没记。
// 造法：先用同一个来源（gift_card, 码 id）发一笔流量包，兑换时余额先记账、再发流量包撞上「同来源已发过」。
// tx.Manager 嵌套不开保存点，所以必须用真提交环境才验证得了「转换之后失败」的回滚。
func TestRedeemRollsBackAfterCodeTransition(t *testing.T) {
	e, ctx := newCommitEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("3"), TrafficBytes: gib}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]
	codeID := e.codeID(t, ctx, code)
	other := e.mkUser(t, ctx)
	if _, err := e.subs.GrantTrafficPack(ctx, subsvc.PackGrant{
		UserID: other, Source: subsvc.PackSourceGiftCard, SourceID: codeID, Bytes: 1,
	}); err != nil {
		t.Fatalf("预先占用来源: %v", err)
	}

	user := e.mkUser(t, ctx)
	_, err := e.redeem(ctx, user, code, nil)
	if err == nil {
		t.Fatal("发放撞上同来源的流量包应失败")
	}
	if st := e.codeStatus(t, ctx, code); st != CodeUnused {
		t.Fatalf("回滚后码状态 %s，期望 unused", st)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_redemptions`); n != 0 {
		t.Fatalf("兑换流水 %d 条，期望 0", n)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'gift_card_balance'`); n != 0 {
		t.Fatalf("赠送交易 %d 笔，期望 0", n)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), decimal.Zero)
}

// 同一用户、同一模板（每人限 1 次）的两张不同的码并发兑：咨询锁让它们排队，恰好成功一次。
func TestConcurrentRedeemSameUserTemplateLimit(t *testing.T) {
	e, ctx := newCommitEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("2")},
		Limits: Limits{MaxUsePerUser: 1}})
	codes := e.mkCodes(t, ctx, tpl.ID, 4, nil)
	user := e.mkUser(t, ctx)

	out := make([]error, len(codes))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, out[i] = e.redeem(ctx, user, codes[i], nil)
		}(i)
	}
	close(start)
	wg.Wait()

	ok := 0
	for _, err := range out {
		switch {
		case err == nil:
			ok++
		case errs.As(err).Code == errs.ValidationFailed:
		default:
			t.Fatalf("并发兑换意外错误: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("成功 %d 次，期望恰好 1 次", ok)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), dec("2"))
	if n := e.count(t, ctx, `SELECT count(*) FROM gift_card_codes WHERE template_id = ? AND status = 'used'`, tpl.ID); n != 1 {
		t.Fatalf("用掉的码 %d 张，期望 1", n)
	}
}

// 换掉的那份订阅上有没完结的续费或换套餐单（order 的 RemainingValue 回 409）：
// 预览不给「换掉」；兑换原样回 409，码仍是 unused、订阅没换、余额没变。
func TestRedeemPlanChangeBlockedByOpenOrder(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	oldPlan, oldVersion, oldPrice := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, oldPlan, oldVersion, oldPrice)
	newPlan, _, _ := e.mkPlan(t, ctx, 5000)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &newPlan}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]
	e.svc.UsePlanChangeValuer(fakeValuer{err: errs.Newf(errs.Conflict, "你的订阅还有未完成的续费或变更套餐订单，请先支付或取消后再兑换")})

	pv, err := e.svc.Preview(asUser(ctx, user), code)
	if err != nil {
		t.Fatalf("预览: %v", err)
	}
	for _, o := range pv.Placement.Options {
		if o.Kind == KindChange {
			t.Fatalf("有未完结订单时预览不该给「换掉」: %+v", o)
		}
	}
	if pv.Placement.DefaultKey != "" && pv.Placement.DefaultKey != KindNew {
		t.Fatalf("默认项 %q", pv.Placement.DefaultKey)
	}

	_, err = e.redeem(ctx, user, code, &Choice{Kind: KindChange, SubscriptionID: &sub.ID})
	mustCode(t, err, errs.Conflict)
	if st := e.codeStatus(t, ctx, code); st != CodeUnused {
		t.Fatalf("码状态 %s，期望 unused", st)
	}
	if after := e.subOf(t, ctx, sub.ID); after.PlanID != oldPlan {
		t.Fatalf("订阅套餐被换成了 %s", after.PlanID)
	}
	mustEq(t, "余额", e.balanceOf(t, ctx, user), decimal.Zero)

	// 续同款不受影响：同一用户另开的同款卡照样能续
	same := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &oldPlan}})
	sc := e.mkCodes(t, ctx, same.ID, 1, nil)[0]
	if _, err := e.redeem(ctx, user, sc, &Choice{Kind: KindRenew, SubscriptionID: &sub.ID}); err != nil {
		t.Fatalf("续同款: %v", err)
	}
}

// 续同款不要求套餐在售：套餐下架后，存量用户用已发出去的同款卡照样能续；新开不行。
func TestRenewAllowedAfterPlanArchived(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	planID, versionID, priceID := e.mkPlan(t, ctx, 3000)
	sub := e.mkSub(t, ctx, user, planID, versionID, priceID)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypePlan, Rewards: Rewards{PlanID: &planID}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)
	e.mustExec(t, ctx, `UPDATE plans SET status = 'archived' WHERE id = ?`, planID)

	if _, err := e.redeem(ctx, user, codes[0], &Choice{Kind: KindRenew, SubscriptionID: &sub.ID}); err != nil {
		t.Fatalf("下架后续同款: %v", err)
	}
	stranger := e.mkUser(t, ctx)
	_, err := e.redeem(ctx, stranger, codes[1], nil)
	if !errors.Is(err, errPlanUnavailable) {
		t.Fatalf("下架后新开应回 errPlanUnavailable，得到 %v", err)
	}
	if st := e.codeStatus(t, ctx, codes[1]); st != CodeUnused {
		t.Fatalf("新开被拒后码状态 %s，期望 unused", st)
	}
}

// 过期码那条路径：expired 已提交、回 ErrCodeUnusable，幂等认领没有被完成（中间件随后释放），
// 释放后同一个键可以接管重试，不会卡在「处理中」。
func TestRedeemExpiredCodeLeavesClaimReleasable(t *testing.T) {
	e, ctx := newCommitEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	code := e.mkCodes(t, ctx, tpl.ID, 1, nil)[0]
	e.mustExec(t, ctx, `UPDATE gift_card_codes SET expires_at = now() - interval '1 minute' WHERE code_hash = ?`, e.svc.hashCode(code))
	user := e.mkUser(t, ctx)
	uctx := asUser(ctx, user)

	fp := idemsvc.Fingerprint{1, 2, 3}
	claim, replay, err := e.idem.Claim(uctx, "gift_card_redeem", "key-expired-1", fp)
	if err != nil || replay != nil || claim == nil {
		t.Fatalf("认领: %v %v", replay, err)
	}
	reply := func(RedeemResult) (idemsvc.Response, error) { return idemsvc.JSON(http.StatusOK, map[string]any{}) }
	_, err = e.svc.Redeem(idemsvc.WithClaim(uctx, claim), RedeemInput{Code: code, Reply: reply})
	if !errors.Is(err, ErrCodeUnusable) {
		t.Fatalf("过期码应回 ErrCodeUnusable，得到 %v", err)
	}
	if claim.Completed() {
		t.Fatal("失败的兑换不该完成幂等认领")
	}
	if st := e.codeStatus(t, ctx, code); st != CodeExpired {
		t.Fatalf("码状态 %s，期望 expired 已提交", st)
	}
	if err := e.idem.Release(ctx, claim, http.StatusUnprocessableEntity); err != nil {
		t.Fatalf("释放认领: %v", err)
	}
	again, replay, err := e.idem.Claim(uctx, "gift_card_redeem", "key-expired-1", fp)
	if err != nil || replay != nil || again == nil {
		t.Fatalf("释放后同键应能接管重试: %v %v", replay, err)
	}
}

// 恢复停用的码：disabled → unused 后能兑；对已过期的码回 409。
func TestToggleCodeRestore(t *testing.T) {
	e, ctx := newEnv(t)
	tpl := e.mkTemplate(t, ctx, TemplateInput{Type: TypeGeneral, Rewards: Rewards{Balance: dec("1")}})
	codes := e.mkCodes(t, ctx, tpl.ID, 2, nil)
	admin := e.asAdmin(ctx)
	id := e.codeID(t, ctx, codes[0])

	if err := e.svc.ToggleCode(admin, id, true); err != nil {
		t.Fatalf("停用: %v", err)
	}
	mustCode(t, e.svc.ToggleCode(admin, id, true), errs.Conflict)
	if err := e.svc.ToggleCode(admin, id, false); err != nil {
		t.Fatalf("恢复: %v", err)
	}
	if st := e.codeStatus(t, ctx, codes[0]); st != CodeUnused {
		t.Fatalf("恢复后状态 %s", st)
	}
	user := e.mkUser(t, ctx)
	if _, err := e.redeem(ctx, user, codes[0], nil); err != nil {
		t.Fatalf("恢复后兑换: %v", err)
	}

	e.mustExec(t, ctx, `UPDATE gift_card_codes SET status = 'expired' WHERE id = ?`, e.codeID(t, ctx, codes[1]))
	mustCode(t, e.svc.ToggleCode(admin, e.codeID(t, ctx, codes[1]), false), errs.Conflict)
	mustCode(t, e.svc.ToggleCode(admin, uuid.Must(uuid.NewV7()), true), errs.NotFound)
}
