package billing

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/purchase"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// checkPlanChangeEntriesPG18 是 TestSubscriptionPeriodPG18 的子测试（sub_period 域），覆盖用户
// 2026-10-07 定的「换套餐的三个入口统一」：
//
//  1. 后台人工开单（赠送）遇到不同套餐：在原订阅上换套餐，订阅仍是一条、链接不变；新价算 0 元，
//     原套餐剩余价值按门户规则折算、全额退进余额；订单（upgrade、管理员代开）、审计、幂等
//     记录与履约在同一事务；
//  2. 后台人工开单（待支付 / 线下已收款）：剩余价值先抵新价，补差价后换套餐，不退余额；
//  3. 套餐卡兑换不同套餐（真实的礼品卡兑换）：同上，退余额金额与门户试算一致，证据挂在卡密上；
//  4. 00134 的证据守卫：没有订单、也没有兑换流水的 plan_changed 事件在提交时被拒；
//  5. 已取消的订阅不放开：后台开单照旧新开订阅。
func checkPlanChangeEntriesPG18(t *testing.T, p *subPeriodPG18, conn *pgx.Conn) {
	ctx := p.ctx
	planA, priceA := p.seedPlan("pc-basic", 1000, 1_000_000)
	planB, priceB := p.seedPlan("pc-pro", 3000, 2_000_000)
	operator := p.fx.referrer

	newUser := func(label string) string {
		id := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, p.fx.tenant, "pc-"+label+"-"+id[:8]+"@example.test", "Plan change "+label)
		return id
	}
	buyA := func(user, label string) string {
		claim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, user, CheckoutIdempotencyScope, label)
		order := mustOrder(t, label)(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
			UserID: user, PlanID: planA, PriceID: priceA, Claim: claim,
		}))
		return p.pay(label, order.OrderID, 1000).SubscriptionID
	}
	scalar := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := p.admin.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("query %s: %v", sql, err)
		}
		return v
	}
	balance := func(user string) int64 {
		return scalar(`SELECT coalesce(sum(CASE e.direction WHEN 'credit' THEN e.amount ELSE -e.amount END), 0)::bigint
			  FROM ledger_entries e JOIN ledger_accounts a ON a.id = e.account_id
			 WHERE a.tenant_id = $1 AND a.account_type = 'user_balance' AND a.owner_user_id = $2::uuid
			   AND a.currency = 'CNY'`, p.fx.tenant, user)
	}
	subsOf := func(user string) int64 {
		return scalar(`SELECT count(*) FROM subscriptions WHERE user_id = $1::uuid`, user)
	}
	planOf := func(sub string) string {
		var plan string
		if err := p.admin.QueryRow(ctx, `SELECT plan_id::text FROM subscriptions WHERE id = $1::uuid`, sub).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	// preview 是门户改套餐的试算：同一份折算，用来核对后台与套餐卡算出的剩余价值
	preview := func(user, sub string) int64 {
		t.Helper()
		q, err := p.billing.PreviewPlanChange(ctx, p.fx.tenant, PlanChangeInput{
			UserID: user, SubscriptionID: sub, PlanID: planB, PriceID: priceB})
		if err != nil || q.ProrationCredit <= 0 || q.ProrationCredit > 1000 {
			t.Fatalf("plan change preview=%+v err=%v", q, err)
		}
		return q.ProrationCredit
	}
	// 下单与试算之间过去了几毫秒，剩余价值向下取整可能少 1 分
	near := func(got, want int64) bool { return got == want || got == want-1 }
	// manual 后台开单：落点由人选（购买模型统一），target 是要换掉的那一份，空串表示不带
	// （只有「另开一份」一个选项时可以不带）
	manual := func(user, target, settlement, reference, label string) *CreateOrderOutput {
		claim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, operator, CheckoutIdempotencyScope, label)
		var choice *purchase.Choice
		if target != "" {
			choice = &purchase.Choice{Kind: purchase.KindChange, SubscriptionID: target}
		}
		return mustOrder(t, label)(p.billing.CreateManualOrder(ctx, p.fx.tenant, CreateManualOrderInput{
			UserID: user, PlanID: planB, PriceID: priceB, Reason: "后台换套餐测试：" + label,
			ActorID: operator, Settlement: settlement, Reference: reference, Claim: claim, Target: choice,
		}))
	}
	type orderRow struct {
		kind, sub, createdBy, claimStatus string
		credit, discount, total           int64
		claimCode                         *int
		audits, refunds, refundAmount     int64
	}
	readOrder := func(orderID string) orderRow {
		t.Helper()
		var o orderRow
		if err := p.admin.QueryRow(ctx, `
			SELECT o.kind, coalesce(o.subscription_id::text, ''), coalesce(o.created_by::text, ''),
			       o.proration_credit_amount, o.discount_amount, o.total_amount,
			       k.status, k.response_code,
			       (SELECT count(*) FROM audit_events a WHERE a.action = 'order.manual_created'
			          AND a.resource_id = o.id AND (a.after_digest->>'plan_change')::boolean),
			       (SELECT count(*) FROM ledger_transactions l WHERE l.source_type = 'order'
			          AND l.source_id = o.id AND l.kind = 'plan_change_refund'),
			       coalesce((SELECT sum(e.amount) FROM ledger_transactions l
			          JOIN ledger_entries e ON e.transaction_id = l.id AND e.direction = 'credit'
			         WHERE l.source_type = 'order' AND l.source_id = o.id
			           AND l.kind = 'plan_change_refund'), 0)::bigint
			  FROM orders o JOIN idempotency_keys k ON k.id = o.idempotency_key_id
			 WHERE o.id = $1::uuid`, orderID).Scan(&o.kind, &o.sub, &o.createdBy, &o.credit,
			&o.discount, &o.total, &o.claimStatus, &o.claimCode, &o.audits, &o.refunds,
			&o.refundAmount); err != nil {
			t.Fatalf("read order %s: %v", orderID, err)
		}
		return o
	}

	// 1) 后台赠送不同套餐：原订阅换成 B，链接不变，剩余价值全额退进余额
	u1 := newUser("grant")
	s1 := buyA(u1, "pc-grant-buy")
	token1 := p.rotate(u1, s1)
	want1 := preview(u1, s1)
	bal1 := balance(u1)
	grant := manual(u1, s1, ManualSettlementGrant, "", "pc-manual-grant")
	o1 := readOrder(grant.OrderID)
	if grant.Status != "fulfilled" || grant.TotalAmount != 0 || grant.DiscountAmount != 3000 ||
		o1.kind != "upgrade" || o1.sub != s1 || o1.createdBy != operator || o1.discount != 3000 ||
		o1.total != 0 || !near(o1.credit, want1) || o1.audits != 1 || o1.claimStatus != "succeeded" ||
		o1.claimCode == nil || *o1.claimCode != 201 || o1.refunds != 1 || o1.refundAmount != o1.credit {
		t.Fatalf("manual grant plan change out=%+v order=%+v want credit≈%d", grant, o1, want1)
	}
	if subsOf(u1) != 1 || planOf(s1) != planB || balance(u1)-bal1 != o1.credit {
		t.Fatalf("manual grant: subs=%d plan=%s balance +%d want +%d",
			subsOf(u1), planOf(s1), balance(u1)-bal1, o1.credit)
	}
	p.aligned("manual grant plan change", s1)
	pulled, err := p.pull(token1)
	if err != nil || pulled.Cred == nil || pulled.Cred.SubscriptionID != s1 {
		t.Fatalf("link after manual plan change: pull=%+v err=%v", pulled, err)
	}
	var versionB string
	if err := p.admin.QueryRow(ctx, `SELECT current_version_id::text FROM plans WHERE id=$1::uuid`, planB).Scan(&versionB); err != nil {
		t.Fatal(err)
	}
	if pulled.Cred.PlanVersionID != versionB {
		t.Fatalf("same link must now serve plan B's version, got %s want %s", pulled.Cred.PlanVersionID, versionB)
	}
	var body map[string]any
	if err := json.Unmarshal(grant.PreparedResponse().BodyBytes(), &body); err != nil ||
		jsonInt(body["balance_refund"]) != o1.credit || jsonInt(body["proration_credit"]) != o1.credit {
		t.Fatalf("manual plan change response=%s err=%v", grant.PreparedResponse().BodyBytes(), err)
	}
	t.Log("marker=plan_change_entries_pg18_manual_grant_ok")

	// 2) 待用户支付：剩余价值先抵新价，付完差价换套餐，不退余额
	u2 := newUser("pending")
	s2 := buyA(u2, "pc-pending-buy")
	want2 := preview(u2, s2)
	pending := manual(u2, s2, ManualSettlementPending, "", "pc-manual-pending")
	o2 := readOrder(pending.OrderID)
	if pending.Status != "pending_payment" || o2.kind != "upgrade" || o2.sub != s2 || !near(o2.credit, want2) ||
		pending.PayableAmount != 3000-o2.credit || o2.audits != 1 {
		t.Fatalf("manual pending plan change out=%+v order=%+v", pending, o2)
	}
	bal2 := balance(u2)
	p.pay("pc-manual-pending", pending.OrderID, pending.PayableAmount)
	if o2 = readOrder(pending.OrderID); o2.refunds != 0 || subsOf(u2) != 1 || planOf(s2) != planB || balance(u2) != bal2 {
		t.Fatalf("paid manual plan change order=%+v subs=%d plan=%s", o2, subsOf(u2), planOf(s2))
	}
	p.aligned("manual pending plan change", s2)

	// 线下已收款：建单事务里按线下渠道结清并换套餐
	u3 := newUser("offline")
	s3 := buyA(u3, "pc-offline-buy")
	offline := manual(u3, s3, ManualSettlementOffline, "PC-OFFLINE-"+p.fx.suffix[:8], "pc-manual-offline")
	if o3 := readOrder(offline.OrderID); offline.Status != "fulfilled" || o3.kind != "upgrade" || o3.sub != s3 ||
		o3.refunds != 0 || subsOf(u3) != 1 || planOf(s3) != planB ||
		scalar(`SELECT count(*) FROM payments WHERE order_id = $1::uuid`, offline.OrderID) != 1 {
		t.Fatalf("manual offline plan change out=%+v order=%+v", offline, o3)
	}
	t.Log("marker=plan_change_entries_pg18_manual_paid_ok")

	// 3) 套餐卡兑换不同套餐：真实兑换流程，退余额金额与门户试算一致
	u4 := newUser("card")
	s4 := buyA(u4, "pc-card-buy")
	token4 := p.rotate(u4, s4)
	templateID, codeID := uuid.NewString(), uuid.NewString()
	code := "PCCARD" + strings.ToUpper(strings.ReplaceAll(p.fx.suffix, "-", ""))[:10]
	p.must(`INSERT INTO gift_card_templates(id,tenant_id,name,type,rewards) VALUES($1,$2,$3,'plan',$4::jsonb)`,
		templateID, p.fx.tenant, "换套餐卡 "+p.fx.suffix[:8],
		`{"plan_id":"`+planB+`","price_id":"`+priceB+`"}`)
	p.must(`INSERT INTO gift_card_codes(id,tenant_id,template_id,code) VALUES($1,$2,$3,$4)`,
		codeID, p.fx.tenant, templateID, code)
	want4 := preview(u4, s4)
	bal4 := balance(u4)
	cards := giftcard.New(p.app, slog.New(slog.NewTextHandler(io.Discard, nil)), p.billing.GiftGranter())
	// 预览「换掉这一份」写出不保留的赠送天数（用户 10-08）：刚买的一期全是付费的，没有赠送天数；
	// 夹具把到期日往后推 7 天（等同加时长卡，没有付费单）后，预览给 7
	giftDays := func() int {
		t.Helper()
		pv, err := cards.PreviewCode(ctx, p.fx.tenant, u4, code)
		if err != nil || pv.Placement == nil {
			t.Fatalf("preview plan card=%+v err=%v", pv, err)
		}
		for _, o := range pv.Placement.Options {
			if o.Kind == purchase.KindChange && o.SubscriptionID == s4 {
				return o.GiftDaysLost
			}
		}
		t.Fatalf("preview has no change option for %s: %+v", s4, pv.Placement.Options)
		return -1
	}
	if got := giftDays(); got != 0 {
		t.Fatalf("paid-only subscription previews %d gifted days", got)
	}
	p.must(`UPDATE subscriptions SET current_period_end = current_period_end + interval '7 days' WHERE id = $1::uuid`, s4)
	if got := giftDays(); got != 7 {
		t.Fatalf("after a 7-day extension the preview says %d gifted days, want 7", got)
	}
	t.Log("marker=plan_change_entries_pg18_gift_days_preview_ok")
	// 不同款套餐卡不预选：用户选「换掉这一份」
	res, err := cards.Redeem(ctx, p.fx.tenant, u4, code,
		&purchase.Choice{Kind: purchase.KindChange, SubscriptionID: s4})
	if err != nil || len(res.Summary) != 2 || !strings.Contains(res.Summary[0], "订阅链接不变") ||
		!strings.Contains(res.Summary[1], "已退回余额") {
		t.Fatalf("plan card redeem=%+v err=%v", res, err)
	}
	refund := balance(u4) - bal4
	var eventRefund, eventCredit, refundTxns int64
	var granted []byte
	if err := p.admin.QueryRow(ctx, `
		SELECT (e.payload->>'balance_refund')::bigint, (e.payload->>'proration_credit')::bigint,
		       (SELECT count(*) FROM ledger_transactions l WHERE l.source_type = 'gift_card_code'
		          AND l.source_id = $2::uuid AND l.kind = 'plan_change_refund'),
		       (SELECT r.granted FROM gift_card_redemptions r WHERE r.code_id = $2::uuid)
		  FROM subscription_events e
		 WHERE e.subscription_id = $1::uuid AND e.event_type = 'plan_changed' AND e.order_id IS NULL
		   AND e.payload->>'gift_card_code_id' = $2::uuid::text`, s4, codeID).Scan(&eventRefund, &eventCredit,
		&refundTxns, &granted); err != nil {
		t.Fatalf("read plan card evidence: %v", err)
	}
	var record map[string]any
	_ = json.Unmarshal(granted, &record)
	if !near(refund, want4) || eventRefund != refund || eventCredit != refund || refundTxns != 1 ||
		record["plan_mode"] != "changed" || jsonInt(record["plan_refund"]) != refund ||
		subsOf(u4) != 1 || planOf(s4) != planB {
		t.Fatalf("plan card change: refund=%d want≈%d event=%d/%d txns=%d record=%v subs=%d plan=%s",
			refund, want4, eventRefund, eventCredit, refundTxns, record, subsOf(u4), planOf(s4))
	}
	p.aligned("plan card change", s4)
	if pulled, err := p.pull(token4); err != nil || pulled.Cred == nil || pulled.Cred.PlanVersionID != versionB {
		t.Fatalf("link after plan card change: pull=%+v err=%v", pulled, err)
	}
	t.Log("marker=plan_change_entries_pg18_plan_card_ok")

	// 4) 证据守卫：没有订单、也没有兑换流水的 plan_changed 事件在提交时被拒
	for label, payload := range map[string]string{
		"no source":          `{}`,
		"unredeemed code":    `{"source":"gift_card","gift_card_code_id":"` + uuid.NewString() + `","balance_refund":0}`,
		"code of a stranger": `{"source":"gift_card","gift_card_code_id":"` + codeID + `","balance_refund":0}`,
	} {
		err := p.app.InTx(ctx, platformdb.Scope{TenantID: p.fx.tenant, ActorID: u1}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO subscription_events
				(tenant_id, subscription_id, event_type, from_status, to_status, actor_kind, payload)
				VALUES ($1, $2::uuid, 'plan_changed', 'active', 'active', 'system', $3::jsonb)`,
				p.fx.tenant, s1, payload)
			return err
		})
		if orderReleasePG18SQLState(err) != "23514" {
			t.Fatalf("forged plan_changed event (%s) err=%v want check_violation", label, err)
		}
	}
	t.Log("marker=plan_change_entries_pg18_evidence_guard_ok")

	// 5) 已取消的订阅不放开：后台开单照旧新开一条订阅
	u5 := newUser("cancelled")
	s5 := buyA(u5, "pc-cancelled-buy")
	p.must(`UPDATE subscriptions SET status = 'cancelled' WHERE id = $1::uuid`, s5)
	fresh := manual(u5, "", ManualSettlementGrant, "", "pc-manual-cancelled")
	if o5 := readOrder(fresh.OrderID); o5.kind != "new" || subsOf(u5) != 2 || planOf(s5) != planA {
		t.Fatalf("cancelled subscription: order=%+v subs=%d plan=%s", o5, subsOf(u5), planOf(s5))
	}
	t.Log("marker=plan_change_entries_pg18_cancelled_stays_closed_ok")
}

// jsonInt 把 JSON 解出来的数字转成整数，不是数字时回 -1。
func jsonInt(v any) int64 {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return -1
}
