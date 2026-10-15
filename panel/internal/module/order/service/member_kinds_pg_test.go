package service

import (
	"testing"
)

// member_kinds_pg_test：会员折扣在续费、流量包、流量重置单上的建单 + 付款。
// 重置单永远按原价收（用户 10-14 定），不打会员折扣；续费与流量包照旧打。

// TestMemberDiscountRenewalPaid 续费单按会员折扣打折，PaidHook 的佣金基数是打折后的总额。
func TestMemberDiscountRenewalPaid(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	hook := &capturePaid{}
	e.svc.UsePaidHook(hook)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	sub, _, _, _ := e.liveSub(t, ctx, user, 3000)

	c, err := e.svc.CreateRenewalOrder(asUser(ctx, user), RenewalOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("续费建单: %v", err)
	}
	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "mk-"+randHex(8)); !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.Status != StatusFulfilled {
		t.Fatalf("订单 %s", o.Status)
	}
	mustEq(t, "member_discount_amount", o.MemberDiscountAmount, dec("3"))
	mustEq(t, "discount_amount", o.DiscountAmount, dec("3"))
	mustEq(t, "total_amount", o.TotalAmount, dec("27"))

	if len(hook.got) != 1 {
		t.Fatalf("PaidHook 调用 %d 次，期望 1", len(hook.got))
	}
	mustEq(t, "PaidHook Total", hook.got[0].Total, dec("27"))
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountPackPaid 流量包单按会员折扣打折，PaidHook 的佣金基数是打折后的总额。
func TestMemberDiscountPackPaid(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	hook := &capturePaid{}
	e.svc.UsePaidHook(hook)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	sub, _, _, _ := e.liveSub(t, ctx, user, 3000)
	packID := e.mkPack(t, ctx, 2000, 10*gib)

	c, err := e.svc.CreatePackOrder(asUser(ctx, user), PackOrderInput{PackID: packID, SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("流量包建单: %v", err)
	}
	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "mk-"+randHex(8)); !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}
	o := e.order(t, ctx, c.OrderID)
	if o.Status != StatusFulfilled {
		t.Fatalf("订单 %s", o.Status)
	}
	mustEq(t, "member_discount_amount", o.MemberDiscountAmount, dec("2"))
	mustEq(t, "discount_amount", o.DiscountAmount, dec("2"))
	mustEq(t, "total_amount", o.TotalAmount, dec("18"))

	if len(hook.got) != 1 {
		t.Fatalf("PaidHook 调用 %d 次，期望 1", len(hook.got))
	}
	mustEq(t, "PaidHook Total", hook.got[0].Total, dec("18"))
	e.assertNoDrift(t, ctx)
}

// TestMemberDiscountNotOnResetOrder 重置单永远按原价收（用户 10-14 定）：有会员折扣时报价与订单上的
// 折扣都是 0、total 为原价，PaidHook 的佣金基数也是原价；付款后已用量清零。
func TestMemberDiscountNotOnResetOrder(t *testing.T) {
	e, ctx := newEnv(t)
	code := payCode()
	e.mkPayProvider(t, ctx, code)
	hook := &capturePaid{}
	e.svc.UsePaidHook(hook)
	user := e.mkUser(t, ctx)
	e.setMemberDiscount(t, ctx, user, 10)
	sub, planID, _, _ := e.liveSub(t, ctx, user, 3000)
	e.setResetPrice(t, ctx, planID, 800)
	e.mustExec(t, ctx, `UPDATE quota_balances SET consumed = ? WHERE subscription_id = ? AND metric = 'traffic.bytes' AND period = 'cycle'`,
		5*gib, sub.ID)

	res, err := e.svc.Quote(asUser(ctx, user), QuoteInput{Action: QuoteReset, SubscriptionID: &sub.ID})
	if err != nil || len(res.Quotes) != 1 {
		t.Fatalf("报价 %+v %v", res, err)
	}
	q := res.Quotes[0]
	if !q.MemberDiscount.IsZero() || q.MemberPercent != 0 {
		t.Fatalf("报价 MemberDiscount=%s MemberPercent=%d，应都为 0", q.MemberDiscount, q.MemberPercent)
	}
	mustEq(t, "报价 Total", q.Total, dec("8"))

	c, err := e.svc.CreateResetOrder(asUser(ctx, user), ResetOrderInput{SubscriptionID: sub.ID})
	if err != nil {
		t.Fatalf("建单: %v", err)
	}
	o := e.order(t, ctx, c.OrderID)
	mustEq(t, "member_discount_amount", o.MemberDiscountAmount, dec("0"))
	mustEq(t, "discount_amount", o.DiscountAmount, dec("0"))
	mustEq(t, "total_amount", o.TotalAmount, dec("8"))

	if ack := e.payAndNotify(t, ctx, user, c.OrderID, code, "mk-"+randHex(8)); !ackOK(ack) {
		t.Fatalf("回执: %s", ack.Body)
	}
	if st := e.order(t, ctx, c.OrderID).Status; st != StatusFulfilled {
		t.Fatalf("订单 %s", st)
	}
	if len(hook.got) != 1 {
		t.Fatalf("PaidHook 调用 %d 次，期望 1", len(hook.got))
	}
	mustEq(t, "PaidHook Total", hook.got[0].Total, dec("8"))
	if e.consumedTraffic(t, ctx, sub.ID) != 0 {
		t.Fatal("consumed 应清零")
	}
	e.assertNoDrift(t, ctx)
}
