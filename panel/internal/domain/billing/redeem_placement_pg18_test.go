package billing

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkPlacementPG18 是 TestSubscriptionPeriodPG18 的子测试（sub_period 域），覆盖购买模型统一的
// 落点（设计稿 6.1「落点」与「后台开单落点」，原型 S5a / S5b）：
//
//  1. 套餐卡同款：两份时选项是 续同款（预选）/ 另开 / 换掉别的；没选回 422 且卡没被用掉；选续 → 续一期；
//  2. 套餐卡不同款：不预选；选项失效回 422 且卡没被用掉；选换掉妈妈那份 → 换套餐、链接不变、退余额；
//  3. 加时长卡、重置卡、送流量卡、盲盒：落到选中的那一份（不一定是默认那份）；
//  4. 只有一份同款：不问，直接续；
//  5. 没有订阅：送流量记为未分配、加时长卡拒绝且卡没用掉；开通第一份时未分配的流量包自动挂上；
//  6. 后台开单：多个选项没带 target 回 422；从订阅行进来预选那一份；跳过可见性、检查 allow_upgrade。
//
// 礼品卡域的库没有计费夹具与运行角色准备，用例放在这里，走真实的 giftcard.Redeem。
func checkPlacementPG18(t *testing.T, p *subPeriodPG18, conn *pgx.Conn) {
	ctx := p.ctx
	planStd, priceStd := p.seedPlan("pl-std", 3000, 1_000_000)
	planAdv, priceAdv := p.seedPlan("pl-adv", 4200, 1_000_000)
	planBasic, priceBasic := p.seedPlan("pl-basic", 1500, 1_000_000)
	cards := giftcard.New(p.app, slog.New(slog.NewTextHandler(io.Discard, nil)), p.billing.GiftGranter())

	newUser := func(label string) string {
		id := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, p.fx.tenant, "pl-"+label+"-"+id[:8]+"@example.test", "Placement "+label)
		return id
	}
	buy := func(user, plan, price, label string, amount int64) string {
		t.Helper()
		order := mustOrder(t, label)(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
			UserID: user, PlanID: plan, PriceID: price, NewCopy: true, Label: label[len(label)-4:],
			Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, user, CheckoutIdempotencyScope, label),
		}))
		return p.pay(label, order.OrderID, amount).SubscriptionID
	}
	seq := 0
	card := func(kind, rewards string) string {
		seq++
		templateID := uuid.NewString()
		code := "PL" + strings.ToUpper(p.fx.suffix[:10]) + string(rune('A'+seq))
		p.must(`INSERT INTO gift_card_templates(id,tenant_id,name,type,rewards) VALUES($1,$2,$3,$4,$5::jsonb)`,
			templateID, p.fx.tenant, "落点卡 "+code, kind, rewards)
		p.must(`INSERT INTO gift_card_codes(tenant_id,template_id,code) VALUES($1,$2,$3)`, p.fx.tenant, templateID, code)
		return code
	}
	planCard := func(plan, price string) string {
		return card("plan", `{"plan_id":"`+plan+`","price_id":"`+price+`"}`)
	}
	codeStatus := func(code string) string {
		var s string
		if err := p.admin.QueryRow(ctx, `SELECT status FROM gift_card_codes WHERE tenant_id=$1 AND code=$2`,
			p.fx.tenant, code).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	keys := func(pl *giftcard.CardPlacement) string {
		var k []string
		for _, o := range pl.Options {
			k = append(k, o.Key)
		}
		return strings.Join(k, ",")
	}
	preview := func(user, code string) *giftcard.CardPlacement {
		t.Helper()
		c, err := cards.PreviewCode(ctx, p.fx.tenant, user, code)
		if err != nil || c.Placement == nil {
			t.Fatalf("preview %s: card=%+v err=%v", code, c, err)
		}
		return c.Placement
	}
	redeem := func(user, code string, choice *purchase.Choice) (*giftcard.RedeemResult, error) {
		return cards.Redeem(ctx, p.fx.tenant, user, code, choice)
	}
	refused := func(step, code string, err error) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || codeStatus(code) != "unused" {
			t.Fatalf("%s: err=%v code status=%s, want 422 and the card unused", step, err, codeStatus(code))
		}
	}
	end := func(sub string) time.Time {
		var e time.Time
		if err := p.admin.QueryRow(ctx, `SELECT current_period_end FROM subscriptions WHERE id=$1::uuid`, sub).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	planOf := func(sub string) string {
		var s string
		if err := p.admin.QueryRow(ctx, `SELECT plan_id::text FROM subscriptions WHERE id=$1::uuid`, sub).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	grantedSub := func(code string) string {
		var s string
		if err := p.admin.QueryRow(ctx, `SELECT coalesce(r.granted->>'subscription_id', '') FROM gift_card_redemptions r
			JOIN gift_card_codes c ON c.id = r.code_id WHERE c.tenant_id=$1 AND c.code=$2`, p.fx.tenant, code).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 小王两份：「我的」标准版（20 天后到期）、「妈妈」基础版（5 天后到期）
	xw := newUser("xiaowang")
	mine := buy(xw, planStd, priceStd, "pl-xw-mine", 3000)
	mom := buy(xw, planBasic, priceBasic, "pl-xw-momx", 1500)
	p.setPeriodEnd(mine, time.Now().UTC().Add(20*24*time.Hour).Truncate(time.Microsecond))
	p.setPeriodEnd(mom, time.Now().UTC().Add(5*24*time.Hour).Truncate(time.Microsecond))
	momLink := p.rotate(xw, mom)

	// 1) 同款套餐卡
	std := planCard(planStd, priceStd)
	pl := preview(xw, std)
	if keys(pl) != "renew:"+mine+",new,change:"+mom || pl.DefaultKey != "renew:"+mine ||
		pl.Question != "怎么用这张卡？" || pl.Options[0].Badge != purchase.BadgeSamePlan ||
		pl.Options[0].NewPeriodEnd == nil || !pl.Options[0].NewPeriodEnd.Equal(end(mine).AddDate(0, 1, 0)) ||
		pl.Options[2].Credit <= 0 {
		t.Fatalf("same plan card placement=%+v", pl)
	}
	_, err := redeem(xw, std, nil)
	refused("same plan card without a choice", std, err)
	before := end(mine)
	if res, err := redeem(xw, std, &purchase.Choice{Kind: purchase.KindRenew, SubscriptionID: mine}); err != nil ||
		!end(mine).Equal(before.AddDate(0, 1, 0)) || grantedSub(std) != mine || !strings.Contains(res.Summary[0], "续费") {
		t.Fatalf("renew card res=%+v err=%v end=%s", res, err, end(mine))
	}

	// 2) 不同款套餐卡：不预选；失效的选择回 422；换掉妈妈那份，链接不变，原套餐没用完的退余额
	adv := planCard(planAdv, priceAdv)
	if pl := preview(xw, adv); pl.DefaultKey != "" || keys(pl) != "new,change:"+mom+",change:"+mine {
		t.Fatalf("different plan card placement=%+v", pl)
	}
	_, err = redeem(xw, adv, &purchase.Choice{Kind: purchase.KindRenew, SubscriptionID: mom})
	refused("stale choice", adv, err)
	if res, err := redeem(xw, adv, &purchase.Choice{Kind: purchase.KindChange, SubscriptionID: mom}); err != nil ||
		planOf(mom) != planAdv || grantedSub(adv) != mom || len(res.Summary) != 2 {
		t.Fatalf("change card res=%+v err=%v plan=%s", res, err, planOf(mom))
	}
	if pulled, err := p.pull(momLink); err != nil || pulled.Cred == nil {
		t.Fatalf("mom's link after the plan card change: %+v %v", pulled, err)
	}
	t.Log("marker=placement_pg18_plan_cards_ok")

	// 3) 加时长卡：预选最快到期（妈妈那份），选「我的」就加到我的
	days := card("general", `{"expire_days":30}`)
	if pl := preview(xw, days); pl.DefaultKey != "extend_days:"+mom || pl.Question != "加到哪一份？" {
		t.Fatalf("days card placement=%+v", pl)
	}
	before = end(mine)
	if _, err := redeem(xw, days, &purchase.Choice{Kind: purchase.KindExtendDays, SubscriptionID: mine}); err != nil ||
		!end(mine).Equal(before.AddDate(0, 0, 30)) || grantedSub(days) != mine {
		t.Fatalf("days card err=%v end=%s want %s", err, end(mine), before.AddDate(0, 0, 30))
	}
	// 重置卡：预选用得最多的那份；没选回 422；选了就清零那一份
	p.must(`UPDATE quota_balances SET consumed = 900000 WHERE subscription_id=$1::uuid AND period='cycle'`, mine)
	reset := card("general", `{"reset_quota":true}`)
	if pl := preview(xw, reset); pl.DefaultKey != "reset_traffic:"+mine || pl.Options[1].Badge != purchase.BadgeMostUsed {
		t.Fatalf("reset card placement=%+v", pl)
	}
	_, err = redeem(xw, reset, nil)
	refused("reset card without a choice", reset, err)
	if _, err := redeem(xw, reset, &purchase.Choice{Kind: purchase.KindResetTraffic, SubscriptionID: mine}); err != nil ||
		p.cycleConsumed(mine) != 0 {
		t.Fatalf("reset card err=%v consumed=%d", err, p.cycleConsumed(mine))
	}
	// 送流量卡与盲盒：落到选中那一份
	traffic := card("general", `{"traffic_bytes":5000}`)
	if _, err := redeem(xw, traffic, &purchase.Choice{Kind: purchase.KindAddTraffic, SubscriptionID: mom}); err != nil ||
		grantedSub(traffic) != mom {
		t.Fatalf("traffic card err=%v", err)
	}
	mystery := card("mystery", `{"pool":[{"label":"大奖","weight":1,"expire_days":7,"traffic_bytes":2000}]}`)
	before = end(mine)
	if res, err := redeem(xw, mystery, &purchase.Choice{Kind: purchase.KindExtendDays, SubscriptionID: mine}); err != nil ||
		res.PrizeLabel != "大奖" || !end(mine).Equal(before.AddDate(0, 0, 7)) || grantedSub(mystery) != mine {
		t.Fatalf("mystery card res=%+v err=%v", res, err)
	}
	var onMine, onMom int64
	if err := p.admin.QueryRow(ctx, `SELECT
		coalesce(sum(granted_bytes) FILTER (WHERE subscription_id=$1::uuid), 0)::bigint,
		coalesce(sum(granted_bytes) FILTER (WHERE subscription_id=$2::uuid), 0)::bigint
		FROM traffic_pack_grants WHERE user_id=$3::uuid AND source='gift_card'`, mine, mom, xw).Scan(&onMine, &onMom); err != nil ||
		onMine != 2000 || onMom != 5000 {
		t.Fatalf("gift traffic by subscription mine=%d mom=%d err=%v", onMine, onMom, err)
	}
	t.Log("marker=placement_pg18_reward_cards_ok")

	// 4) 只有一份同款：不问
	single := newUser("single")
	only := buy(single, planStd, priceStd, "pl-single-only", 3000)
	one := planCard(planStd, priceStd)
	if pl := preview(single, one); keys(pl) != "renew:"+only || pl.DefaultKey != "renew:"+only {
		t.Fatalf("single same plan placement=%+v", pl)
	}
	if _, err := redeem(single, one, nil); err != nil || grantedSub(one) != only {
		t.Fatalf("single same plan redeem err=%v", err)
	}

	// 5) 没有订阅：送流量记为未分配，加时长卡拒绝；开通第一份时自动挂上
	nobody := newUser("nosub")
	gift := card("general", `{"traffic_bytes":7000}`)
	if pl := preview(nobody, gift); len(pl.Options) != 0 || pl.DefaultKey != "" {
		t.Fatalf("no subscription traffic placement=%+v", pl)
	}
	if _, err := redeem(nobody, gift, nil); err != nil || grantedSub(gift) != "" {
		t.Fatalf("unassigned gift traffic err=%v", err)
	}
	lonely := card("general", `{"expire_days":3}`)
	_, err = redeem(nobody, lonely, nil)
	refused("days card without a subscription", lonely, err)
	first := buy(nobody, planBasic, priceBasic, "pl-nosub-firs", 1500)
	var attached, systemMoves int64
	if err := p.admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM traffic_pack_grants WHERE user_id=$1::uuid AND subscription_id=$2::uuid),
		(SELECT count(*) FROM traffic_pack_transfers WHERE user_id=$1::uuid AND to_subscription_id=$2::uuid
		    AND from_subscription_id IS NULL AND actor_kind='system' AND remaining_bytes=7000)`,
		nobody, first).Scan(&attached, &systemMoves); err != nil || attached != 1 || systemMoves != 1 {
		t.Fatalf("auto attach on first subscription attached=%d moves=%d err=%v", attached, systemMoves, err)
	}
	t.Log("marker=placement_pg18_unassigned_traffic_ok")

	// 6) 后台开单：xw 现在「我的」标准版、「妈妈」进阶版，给他开基础版
	operator := p.fx.referrer
	manual := func(label string, plan, price string, target *purchase.Choice) (*CreateOrderOutput, error) {
		return p.billing.CreateManualOrder(ctx, p.fx.tenant, CreateManualOrderInput{
			UserID: xw, PlanID: plan, PriceID: price, Reason: "后台落点测试：" + label, ActorID: operator,
			Target: target,
			Claim:  orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, operator, CheckoutIdempotencyScope, label),
		})
	}
	_, err = manual("pl-manual-untargeted", planBasic, priceBasic, nil)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Fields["target"] == "" {
		t.Fatalf("manual order without target err=%v", err)
	}
	if pv, err := p.billing.ManualOrderOptions(ctx, p.fx.tenant, xw, planBasic, priceBasic, mine); err != nil ||
		pv.DefaultKey != "change:"+mine || len(pv.Options) != 3 {
		t.Fatalf("manual preview from the row=%+v err=%v", pv, err)
	}
	if pv, err := p.billing.ManualOrderOptions(ctx, p.fx.tenant, xw, planBasic, priceBasic, ""); err != nil || pv.DefaultKey != "" {
		t.Fatalf("manual preview without entry=%+v err=%v", pv, err)
	}
	// 后台开单不看可见性：隐藏的套餐照样能换过去
	p.must(`UPDATE plans SET visibility='hidden' WHERE id=$1::uuid`, planBasic)
	if out, err := manual("pl-manual-hidden", planBasic, priceBasic,
		&purchase.Choice{Kind: purchase.KindChange, SubscriptionID: mine}); err != nil || out.Status != "fulfilled" ||
		planOf(mine) != planBasic {
		t.Fatalf("manual change to a hidden plan out=%+v err=%v", out, err)
	}
	p.must(`UPDATE plans SET visibility='public' WHERE id=$1::uuid`, planBasic)
	// 但要查允许变更
	p.must(`UPDATE plans SET allow_upgrade=false WHERE id=$1::uuid`, planStd)
	_, err = manual("pl-manual-no-upgrade", planStd, priceStd,
		&purchase.Choice{Kind: purchase.KindChange, SubscriptionID: mine})
	if !errors.As(err, &he) || he.Code != httpx.CodeConflict {
		t.Fatalf("manual change to a plan without allow_upgrade err=%v", err)
	}
	p.must(`UPDATE plans SET allow_upgrade=true WHERE id=$1::uuid`, planStd)
	t.Log("marker=placement_pg18_manual_order_ok")
}
