package billing

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkOrderDetailAndPreviewPG18 是 TestSubscriptionPeriodPG18 的子测试（sub_period 域），覆盖购买模型
// 合后小修（w8buyfix）：
//
//  1. 订单详情的 subscription_id：新购是新开的那份，续费、换套餐、流量包是原来那份；还没履约时为 null；
//  2. 后台开单 preview 的 min_payment 与每个落点的 due / below_minimum（与建单 422 同一个判定），
//     以及待支付单低于最低额的 422 带 fields.settlement。
func checkOrderDetailAndPreviewPG18(t *testing.T, p *subPeriodPG18, conn *pgx.Conn) {
	ctx := p.ctx
	planStd, priceStd := p.seedPlan("od-std", 4000, 1_000_000)
	planAdv, priceAdv := p.seedPlan("od-adv", 5000, 1_000_000)
	planCheap, priceCheap := p.seedPlan("od-cheap", 2500, 1_000_000)
	planMid, priceMid := p.seedPlan("od-mid", 4500, 1_000_000)
	packID := uuid.NewString()
	p.must(`INSERT INTO traffic_packs(id,tenant_id,name,traffic_bytes,currency,unit_amount)
		VALUES($1,$2,'OD pack',5000,'CNY',600)`, packID, p.fx.tenant)
	newUser := func(label string) string {
		id := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, p.fx.tenant, "od-"+label+"-"+id[:8]+"@example.test", "Detail "+label)
		return id
	}
	// detailSub 读门户订单详情的 subscription_id（以用户身份、走 RLS），nil 表示 null
	detailSub := func(step, user, orderID string) *string {
		t.Helper()
		d, err := p.billing.MyOrderDetail(ctx, p.fx.tenant, user, orderID)
		if err != nil {
			t.Fatalf("%s: order detail: %v", step, err)
		}
		return d.SubscriptionID
	}
	wantSub := func(step string, got *string, want string) {
		t.Helper()
		if want == "" && got != nil {
			t.Fatalf("%s: subscription_id=%s want null", step, *got)
		}
		if want != "" && (got == nil || *got != want) {
			t.Fatalf("%s: subscription_id=%v want %s", step, got, want)
		}
	}

	// 1a) 新购：待支付时 null，付完是新开的那一份
	u := newUser("buyer")
	order := mustOrder(t, "od-new")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u, PlanID: planStd, PriceID: priceStd,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u, CheckoutIdempotencyScope, "od-new")}))
	wantSub("new order before payment", detailSub("od-new", u, order.OrderID), "")
	sub := p.pay("od-new", order.OrderID, order.PayableAmount).SubscriptionID
	if sub == "" {
		t.Fatal("new order opened no subscription")
	}
	wantSub("new order fulfilled", detailSub("od-new", u, order.OrderID), sub)

	// 1b) 续费：订单行建单时就记着原来那份，但没履约之前详情仍是 null；付完是原来那份
	renewal := mustOrder(t, "od-renew")(p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
		UserID: u, SubscriptionID: sub,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u, RenewalIdempotencyScope, "od-renew")}))
	wantSub("renewal before payment", detailSub("od-renew", u, renewal.OrderID), "")
	p.pay("od-renew", renewal.OrderID, renewal.PayableAmount)
	wantSub("renewal fulfilled", detailSub("od-renew", u, renewal.OrderID), sub)

	// 1c) 换套餐：在原来那份上换，详情是原来那份
	change, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{
		UserID: u, SubscriptionID: sub, PlanID: planAdv, PriceID: priceAdv,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u, PlanChangeIdempotencyScope, "od-change")})
	if err != nil {
		t.Fatalf("plan change: %v", err)
	}
	if change.Status == "pending_payment" {
		wantSub("plan change before payment", detailSub("od-change", u, change.OrderID), "")
		p.pay("od-change", change.OrderID, change.PayableAmount)
	}
	wantSub("plan change fulfilled", detailSub("od-change", u, change.OrderID), sub)

	// 1d) 流量包：加到哪一份，详情就是哪一份
	pack, err := p.billing.CreateTrafficPackOrder(ctx, p.fx.tenant, CreateTrafficPackOrderInput{
		UserID: u, SubscriptionID: sub, PackID: packID,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u, CheckoutIdempotencyScope, "od-pack")})
	if err != nil || pack.Status != "pending_payment" {
		t.Fatalf("pack order=%+v err=%v", pack, err)
	}
	wantSub("pack before payment", detailSub("od-pack", u, pack.OrderID), "")
	p.pay("od-pack", pack.OrderID, pack.PayableAmount)
	wantSub("pack fulfilled", detailSub("od-pack", u, pack.OrderID), sub)

	// 1e) 取消的单（没履约）是 null
	cancelled := mustOrder(t, "od-cancel")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u, PlanID: planCheap, PriceID: priceCheap,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u, CheckoutIdempotencyScope, "od-cancel")}))
	if _, err := p.billing.CancelOrder(ctx, p.fx.tenant, u, cancelled.OrderID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	wantSub("cancelled order", detailSub("od-cancel", u, cancelled.OrderID), "")
	t.Log("marker=order_detail_pg18_subscription_id_ok")

	// 2) 后台开单 preview：站点最低额 ¥30（一个接单的 CNY 渠道），按落点给应付与是否低于最低额
	minProvider := uuid.NewString()
	p.must(`INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled,
		supported_currencies,config) VALUES($1,$2,$3,'demo_hmac','OD min',true,'{CNY}','{"min_amount":3000}')`,
		minProvider, p.fx.tenant, "od-min-"+p.fx.suffix[:8])
	invalidateMinPayment(p.fx.tenant)
	defer func() {
		p.must(`UPDATE payment_providers SET enabled=false WHERE id=$1`, minProvider)
		invalidateMinPayment(p.fx.tenant)
	}()
	v := newUser("manual")
	vOrder := mustOrder(t, "od-manual-buy")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: v, PlanID: planStd, PriceID: priceStd,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, v, CheckoutIdempotencyScope, "od-manual-buy")}))
	vSub := p.pay("od-manual-buy", vOrder.OrderID, vOrder.PayableAmount).SubscriptionID
	preview := func(step, planID, priceID string) map[string]ManualPlacement {
		t.Helper()
		pv, err := p.billing.ManualOrderOptions(ctx, p.fx.tenant, v, planID, priceID, "")
		if err != nil || pv.MinPayment != 3000 {
			t.Fatalf("%s: preview=%+v err=%v", step, pv, err)
		}
		out := map[string]ManualPlacement{}
		for _, o := range pv.Options {
			out[string(o.Kind)] = o
		}
		return out
	}
	// 便宜的 ¥25：另开一份应付 ¥25 < ¥30；换掉那份时剩余价值抵满，应付 0，不拦
	cheap := preview("cheap", planCheap, priceCheap)
	if n, c := cheap[string(purchase.KindNew)], cheap[string(purchase.KindChange)]; n.Due != 2500 || !n.BelowMinimum ||
		c.SubscriptionID != vSub || c.Credit < 2500 || c.Due != 0 || c.BelowMinimum {
		t.Fatalf("cheap preview=%+v", cheap)
	}
	// 同款 ¥40：续一期与另开一份都是 ¥40，不拦
	same := preview("same", planStd, priceStd)
	if r, n := same[string(purchase.KindRenew)], same[string(purchase.KindNew)]; r.Due != 4000 || r.BelowMinimum ||
		n.Due != 4000 || n.BelowMinimum {
		t.Fatalf("same plan preview=%+v", same)
	}
	// ¥45 换掉那份：先抵剩余价值，剩下的不到 ¥30 → 拦；另开一份 ¥45 不拦
	mid := preview("mid", planMid, priceMid)
	if c, n := mid[string(purchase.KindChange)], mid[string(purchase.KindNew)]; c.Credit <= 1500 ||
		c.Due != 4500-c.Credit || !c.BelowMinimum || n.Due != 4500 || n.BelowMinimum {
		t.Fatalf("mid preview=%+v", mid)
	}
	// 建单与 preview 同一个判定：待支付另开 ¥25 回 422，带 fields.settlement（后台落到结算方式上）
	_, err = p.billing.CreateManualOrder(ctx, p.fx.tenant, CreateManualOrderInput{UserID: v,
		PlanID: planCheap, PriceID: priceCheap, Reason: "低于最低额的待支付单", ActorID: p.fx.referrer,
		Settlement: ManualSettlementPending, Target: &purchase.Choice{Kind: purchase.KindNew},
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, p.fx.referrer, CheckoutIdempotencyScope, "od-manual-short")})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["settlement"] == "" {
		t.Fatalf("manual pending below the minimum err=%v", err)
	}
	t.Log("marker=order_detail_pg18_manual_preview_minimum_ok")
}
