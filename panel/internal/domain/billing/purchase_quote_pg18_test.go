package billing

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkPurchaseQuotePG18 是 TestSubscriptionPeriodPG18 的子测试（sub_period 域），覆盖购买模型
// 统一的钱（设计稿 6.1 billing 与 plan_change 行）：
//
//  1. 账平：续费凑最低额（S7c）、余额全付（S7b）、Forced、SmallDue（新购与续费）、换大、换小、
//     换大只差几分钱、流量包，每种都下单并结算：每笔交易借贷相等，冻结都已捕获，恒等式成立；
//  2. 幂等：quote_changed 不占用幂等键，用新报价重提能成功；完成的键不能再建单；
//  3. 并发重复下单：两个请求同时新购同一套餐，只有一张成功，另一张回 order_pending；
//  4. 价格篡改：expect 偏低、use_balance 超过余额、as_of 早于 10 分钟或在未来、price_id 属于
//     别的套餐、订阅属于别人，都拒绝；
//  5. 漂移：报价后余额变了、或原订阅又用了流量（剩余价值按当前用量算），回 quote_changed；
//  6. 换掉一份：链接（凭据与 proxy_uuid）不变；过期 30 天内的换套餐剩余价值为 0；窗口关闭回 409。
func checkPurchaseQuotePG18(t *testing.T, p *subPeriodPG18, conn *pgx.Conn) {
	ctx := p.ctx
	planStd, priceStd := p.seedPlan("pq-std", 3000, 1_000_000)
	planAdv, priceAdv := p.seedPlan("pq-adv", 4200, 1_000_000)
	planBasic, priceBasic := p.seedPlan("pq-basic", 1500, 1_000_000)
	planTiny, priceTiny := p.seedPlan("pq-tiny", 30, 1_000_000)
	planNear, priceNear := p.seedPlan("pq-near", 3020, 1_000_000)
	packID := uuid.NewString()
	p.must(`INSERT INTO traffic_packs(id,tenant_id,name,traffic_bytes,currency,unit_amount)
		VALUES($1,$2,'PQ pack',5000,'CNY',50)`, packID, p.fx.tenant)
	// 站点最低付款额 ¥1.00：一个接单的 CNY 渠道配了 min_amount
	minProvider := uuid.NewString()
	p.must(`INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled,
		supported_currencies,config) VALUES($1,$2,$3,'demo_hmac','PQ min',true,'{CNY}','{"min_amount":100}')`,
		minProvider, p.fx.tenant, "pq-min-"+p.fx.suffix[:8])
	invalidateMinPayment(p.fx.tenant)
	defer func() {
		p.must(`UPDATE payment_providers SET enabled=false WHERE id=$1`, minProvider)
		invalidateMinPayment(p.fx.tenant)
	}()

	newUser := func(label string) string {
		id := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, p.fx.tenant, "pq-"+label+"-"+id[:8]+"@example.test", "Quote "+label)
		return id
	}
	quote := func(in QuoteInput) *QuoteOutput {
		t.Helper()
		out, err := p.billing.Quote(ctx, p.fx.tenant, in)
		if err != nil || len(out.Quotes) == 0 {
			t.Fatalf("quote %+v: out=%+v err=%v", in, out, err)
		}
		return out
	}
	expect := func(out *QuoteOutput, q Quote, withBalance bool) *Expectation {
		b := q.WithoutBalance
		if withBalance {
			b = q.WithBalance
		}
		return &Expectation{AsOf: out.AsOf, Total: q.Total, BalanceApplied: b.Applied, Payable: b.Payable}
	}
	buy := func(user, plan, price, label string, amount int64) string {
		t.Helper()
		order := mustOrder(t, label)(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
			UserID: user, PlanID: plan, PriceID: price,
			Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, user, CheckoutIdempotencyScope, label),
		}))
		return p.pay(label, order.OrderID, amount).SubscriptionID
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
		return scalar(`SELECT coalesce(-sum(balance_signed), 0)::bigint FROM ledger_accounts
			WHERE tenant_id=$1 AND owner_user_id=$2::uuid AND account_type='user_balance' AND currency='CNY'`,
			p.fx.tenant, user)
	}
	link := func(sub string) string {
		var s string
		if err := p.admin.QueryRow(ctx, `SELECT s.proxy_uuid::text || '/' || string_agg(encode(c.token_hash, 'hex'), ',' ORDER BY c.id)
			  FROM subscriptions s JOIN subscription_credentials c ON c.subscription_id = s.id AND c.status = 'active'
			 WHERE s.id = $1::uuid GROUP BY s.proxy_uuid`, sub).Scan(&s); err != nil {
			t.Fatalf("read link of %s: %v", sub, err)
		}
		return s
	}
	wantCode := func(step string, err error, code httpx.Code) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != code {
			t.Fatalf("%s err=%v want %s", step, err, code)
		}
	}

	// 1a) S7c：余额 ¥29.50、续费 ¥30 → 余额只用 ¥29.00，付 ¥1.00，¥0.50 留在余额里
	u1 := newUser("s7c")
	s1 := buy(u1, planStd, priceStd, "pq-s7c-buy", 3000)
	orderReleasePG18FundBalance(t, ctx, p.app, p.fx.tenant, u1, 2950)
	rq := quote(QuoteInput{UserID: u1, Action: QuoteRenew, SubscriptionID: s1})
	q := rq.Quotes[0]
	if rq.MinPayment != 100 || rq.Balance != 2950 || q.Total != 3000 || *q.PriceID != priceStd ||
		q.WithBalance.Applied != 2900 || q.WithBalance.Payable != 100 || q.WithBalance.Kept != 50 ||
		q.WithoutBalance.Payable != 3000 || q.PreviousEnd == nil {
		t.Fatalf("S7c quote=%+v", rq)
	}
	renewed := mustOrder(t, "pq-s7c-renew")(p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
		UserID: u1, SubscriptionID: s1, UseBalance: 2950, Expect: expect(rq, q, true),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u1, RenewalIdempotencyScope, "pq-s7c-renew"),
	}))
	if renewed.BalanceApplied != 2900 || renewed.PayableAmount != 100 || renewed.Status != "pending_payment" {
		t.Fatalf("S7c renewal=%+v", renewed)
	}
	p.pay("pq-s7c-pay", renewed.OrderID, 100)
	if got := balance(u1); got != 50 {
		t.Fatalf("S7c balance left=%d want 50", got)
	}

	// 1b) 流量包只要 ¥0.50、余额 ¥0.50：只能全用余额付（Forced），加到指定那一份
	pq := quote(QuoteInput{UserID: u1, Action: QuotePack, PackID: packID, SubscriptionID: s1})
	q = pq.Quotes[0]
	if !q.WithoutBalance.Forced || q.WithoutBalance.Applied != 50 || q.WithoutBalance.Payable != 0 {
		t.Fatalf("forced pack quote=%+v", pq)
	}
	pack, err := p.billing.CreateTrafficPackOrder(ctx, p.fx.tenant, CreateTrafficPackOrderInput{
		UserID: u1, SubscriptionID: s1, PackID: packID, Expect: expect(pq, q, false),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u1, CheckoutIdempotencyScope, "pq-pack"),
	})
	if err != nil || pack.Status != "fulfilled" || pack.BalanceApplied != 50 {
		t.Fatalf("forced pack order=%+v err=%v", pack, err)
	}
	if n := scalar(`SELECT count(*) FROM traffic_pack_grants WHERE source='order' AND source_id=$1::uuid
		AND subscription_id=$2::uuid`, pack.OrderID, s1); n != 1 {
		t.Fatalf("pack grant on the ordered subscription=%d", n)
	}

	// 1c) S7b：余额够付，另买一份同款（new_copy，起名），当场开通
	u2 := newUser("s7b")
	buy(u2, planStd, priceStd, "pq-s7b-first", 3000)
	orderReleasePG18FundBalance(t, ctx, p.app, p.fx.tenant, u2, 5000)
	nq := quote(QuoteInput{UserID: u2, Action: QuoteNew, PlanID: planStd, NewCopy: true})
	q = nq.Quotes[0]
	if q.WithBalance.Applied != 3000 || q.WithBalance.Payable != 0 {
		t.Fatalf("S7b quote=%+v", nq)
	}
	// 另买同款不起名：App 里会重名 → 422；起名后成功，名字落到新的那一份上
	_, err = p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u2, PlanID: planStd,
		PriceID: priceStd, NewCopy: true, RejectSamePlan: true, UseBalance: 5000,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u2, CheckoutIdempotencyScope, "pq-s7b-nolabel")})
	wantCode("new copy without a label", err, httpx.CodeValidationFailed)
	copyOrder := mustOrder(t, "pq-s7b-copy")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u2, PlanID: planStd, PriceID: priceStd, NewCopy: true, RejectSamePlan: true,
		Label: " 妈妈的 iPad ", UseBalance: 5000, Expect: expect(nq, q, true),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u2, CheckoutIdempotencyScope, "pq-s7b-copy"),
	}))
	if copyOrder.Status != "fulfilled" || copyOrder.BalanceApplied != 3000 ||
		scalar(`SELECT count(*) FROM subscriptions WHERE user_id=$1::uuid AND label='妈妈的 iPad'`, u2) != 1 {
		t.Fatalf("S7b new copy=%+v", copyOrder)
	}

	// 1d) Forced 新购：应付 ¥0.30、余额够 → 关着余额也只能全用余额付
	tq := quote(QuoteInput{UserID: u2, Action: QuoteNew, PlanID: planTiny})
	q = tq.Quotes[0]
	forced := mustOrder(t, "pq-forced")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u2, PlanID: planTiny, PriceID: priceTiny, RejectSamePlan: true, Expect: expect(tq, q, false),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u2, CheckoutIdempotencyScope, "pq-forced"),
	}))
	if !q.WithoutBalance.Forced || forced.Status != "fulfilled" || forced.BalanceApplied != 30 || forced.PayableAmount != 0 {
		t.Fatalf("forced order=%+v quote=%+v", forced, q)
	}

	// 1e) SmallDue：应付 ¥0.30、余额 0 → 免掉，按折扣记账；续费同样免（无券的折扣只许是免单）
	u3 := newUser("smalldue")
	sq := quote(QuoteInput{UserID: u3, Action: QuoteNew, PlanID: planTiny})
	q = sq.Quotes[0]
	if !q.WithoutBalance.SmallDue || q.WithoutBalance.Waived != 30 || q.WithoutBalance.Payable != 0 {
		t.Fatalf("small due quote=%+v", q)
	}
	small := mustOrder(t, "pq-smalldue")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u3, PlanID: planTiny, PriceID: priceTiny, RejectSamePlan: true, Expect: expect(sq, q, false),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u3, CheckoutIdempotencyScope, "pq-smalldue"),
	}))
	if small.Status != "fulfilled" || small.DiscountAmount != 30 || small.TotalAmount != 0 || small.PayableAmount != 0 ||
		scalar(`SELECT count(*) FROM audit_events WHERE resource_id=$1::uuid AND action='order.created'
			AND (after_digest->>'small_due_waived')::bigint=30`, small.OrderID) != 1 {
		t.Fatalf("small due order=%+v", small)
	}
	var tinyID string
	if err := p.admin.QueryRow(ctx, `SELECT id::text FROM subscriptions WHERE user_id=$1::uuid AND plan_id=$2::uuid`,
		u3, planTiny).Scan(&tinyID); err != nil {
		t.Fatalf("small due subscription: %v", err)
	}
	smallRenew := mustOrder(t, "pq-smalldue-renew")(p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
		UserID: u3, SubscriptionID: tinyID,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u3, RenewalIdempotencyScope, "pq-smalldue-renew"),
	}))
	if smallRenew.Status != "fulfilled" || smallRenew.DiscountAmount != 30 || smallRenew.TotalAmount != 0 {
		t.Fatalf("small due renewal=%+v", smallRenew)
	}

	// 1f) 换大补差价、换小退余额、换大只差几分钱（SmallDue 免掉）；链接都不变
	u4 := newUser("upgrade")
	s4 := buy(u4, planStd, priceStd, "pq-up-buy", 3000)
	link4 := link(s4)
	cq := quote(QuoteInput{UserID: u4, Action: QuoteChange, SubscriptionID: s4})
	var up, down *Quote
	for i := range cq.Quotes {
		switch *cq.Quotes[i].PlanID {
		case planAdv:
			up = &cq.Quotes[i]
		case planBasic:
			down = &cq.Quotes[i]
		case planStd:
			t.Fatal("change quote lists the subscription's own plan")
		}
	}
	if up == nil || down == nil || up.Credit <= 0 || up.CreditDetail == nil || up.CreditDetail.Paid != 3000 ||
		up.Total != 4200-up.Credit || down.Refund != down.Credit-1500 || down.Total != 0 {
		t.Fatalf("change quotes=%+v", cq.Quotes)
	}
	upOrder, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{
		UserID: u4, SubscriptionID: s4, PlanID: planAdv, PriceID: priceAdv, Expect: expect(cq, *up, false),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u4, PlanChangeIdempotencyScope, "pq-up"),
	})
	if err != nil || upOrder.PayableAmount != up.Total {
		t.Fatalf("upgrade order=%+v err=%v", upOrder, err)
	}
	p.pay("pq-up-pay", upOrder.OrderID, upOrder.PayableAmount)
	if link(s4) != link4 || scalar(`SELECT count(*) FROM subscriptions WHERE id=$1::uuid AND plan_id=$2::uuid`, s4, planAdv) != 1 {
		t.Fatal("upgrade must keep the link and switch the plan")
	}
	u5 := newUser("downgrade")
	s5 := buy(u5, planStd, priceStd, "pq-down-buy", 3000)
	downOrder, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{
		UserID: u5, SubscriptionID: s5, PlanID: planBasic, PriceID: priceBasic,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u5, PlanChangeIdempotencyScope, "pq-down"),
	})
	if err != nil || downOrder.Status != "fulfilled" || downOrder.BalanceRefund <= 0 || balance(u5) != downOrder.BalanceRefund {
		t.Fatalf("downgrade order=%+v balance=%d err=%v", downOrder, balance(u5), err)
	}
	u6 := newUser("near")
	s6 := buy(u6, planStd, priceStd, "pq-near-buy", 3000)
	near, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{
		UserID: u6, SubscriptionID: s6, PlanID: planNear, PriceID: priceNear,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u6, PlanChangeIdempotencyScope, "pq-near"),
	})
	if err != nil || near.Status != "fulfilled" || near.TotalAmount != 0 || near.DiscountAmount <= 0 ||
		near.DiscountAmount >= 100 || near.BalanceRefund != 0 {
		t.Fatalf("near upgrade (small due) order=%+v err=%v", near, err)
	}
	t.Log("marker=purchase_quote_pg18_orders_ok")

	// 账平：本租户每笔交易借贷相等，已履约单的余额冻结都已捕获，恒等式成立
	if n := scalar(`SELECT count(*) FROM (SELECT e.transaction_id FROM ledger_entries e
		JOIN ledger_transactions l ON l.id = e.transaction_id WHERE l.tenant_id=$1
		GROUP BY e.transaction_id HAVING sum(CASE e.direction WHEN 'debit' THEN e.amount ELSE 0 END)
		  <> sum(CASE e.direction WHEN 'credit' THEN e.amount ELSE 0 END)) x`, p.fx.tenant); n != 0 {
		t.Fatalf("%d unbalanced ledger transactions", n)
	}
	if n := scalar(`SELECT count(*) FROM balance_holds h JOIN orders o ON o.id = h.order_id
		WHERE o.tenant_id=$1 AND o.status='fulfilled' AND h.status <> 'captured'`, p.fx.tenant); n != 0 {
		t.Fatalf("%d fulfilled orders still hold balance", n)
	}
	if n := scalar(`SELECT count(*) FROM orders WHERE tenant_id=$1 AND total_amount <>
		greatest(subtotal_amount - discount_amount - proration_credit_amount, 0) + tax_amount`, p.fx.tenant); n != 0 {
		t.Fatalf("%d orders break the total identity", n)
	}
	t.Log("marker=purchase_quote_pg18_ledger_balanced_ok")

	// 2) 幂等：报价对不上不占用幂等键，同一个键带新报价重提能成功；完成的键不能再建单
	u7 := newUser("idem")
	iq := quote(QuoteInput{UserID: u7, Action: QuoteNew, PlanID: planStd})
	q = iq.Quotes[0]
	idem := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u7, CheckoutIdempotencyScope, "pq-idem")
	low := expect(iq, q, false)
	low.Total--
	_, err = p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u7, PlanID: planStd,
		PriceID: priceStd, RejectSamePlan: true, Expect: low, Claim: idem})
	wantCode("expect below the quote", err, httpx.CodeQuoteChanged)
	idemOrder := mustOrder(t, "pq-idem-retry")(p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: u7, PlanID: planStd, PriceID: priceStd, RejectSamePlan: true, Expect: expect(iq, q, false), Claim: idem}))
	if n := scalar(`SELECT count(*) FROM idempotency_keys WHERE id=$1::uuid AND status='succeeded'
		AND response_code=201 AND resource_id=$2::uuid`, idem.ID, idemOrder.OrderID); n != 1 {
		t.Fatalf("retried claim not completed with the order (n=%d)", n)
	}
	if _, err := p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u7, PlanID: planBasic,
		PriceID: priceBasic, Claim: idem}); err == nil {
		t.Fatal("a completed claim must not create another order")
	}
	t.Log("marker=purchase_quote_pg18_idempotency_ok")

	// 3) 并发重复下单：同一套餐两张同时下，只有一张成功，另一张 order_pending（带那张单的 id）
	u8 := newUser("race")
	claims := []string{"pq-race-a", "pq-race-b"}
	var wg sync.WaitGroup
	results := make([]error, len(claims))
	orders := make([]*CreateOrderOutput, len(claims))
	for i, label := range claims {
		c := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u8, CheckoutIdempotencyScope, label)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			orders[i], results[i] = p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
				UserID: u8, PlanID: planBasic, PriceID: priceBasic, RejectSamePlan: true, NewCopy: true,
				Label: "race-" + label[len(label)-1:], Claim: c})
		}(i)
	}
	wg.Wait()
	ok, pending := 0, 0
	for i, err := range results {
		var he *httpx.Error
		switch {
		case err == nil && orders[i].Status == "pending_payment":
			ok++
		case errors.As(err, &he) && he.Code == httpx.CodeOrderPending && he.Fields["order_id"] != "":
			pending++
		default:
			t.Fatalf("race result %d err=%v", i, err)
		}
	}
	if ok != 1 || pending != 1 || scalar(`SELECT count(*) FROM orders o JOIN order_items i ON i.order_id=o.id
		WHERE o.user_id=$1::uuid AND o.kind='new' AND o.status='pending_payment'`, u8) != 1 {
		t.Fatalf("race ok=%d pending=%d", ok, pending)
	}
	t.Log("marker=purchase_quote_pg18_duplicate_order_ok")

	// 4) 价格篡改
	u9 := newUser("tamper")
	s9 := buy(u9, planStd, priceStd, "pq-tamper-buy", 3000)
	orderReleasePG18FundBalance(t, ctx, p.app, p.fx.tenant, u9, 1000)
	tq = quote(QuoteInput{UserID: u9, Action: QuoteRenew, SubscriptionID: s9})
	q = tq.Quotes[0]
	tamper := func(label string, mutate func(*CreateRenewalInput), code httpx.Code) {
		t.Helper()
		in := CreateRenewalInput{UserID: u9, SubscriptionID: s9, UseBalance: 1000, Expect: expect(tq, q, true),
			Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u9, RenewalIdempotencyScope, label)}
		mutate(&in)
		_, err := p.billing.CreateRenewal(ctx, p.fx.tenant, in)
		wantCode(label, err, code)
	}
	tamper("pq-overbalance", func(in *CreateRenewalInput) {
		in.UseBalance = 999999
		in.Expect.BalanceApplied, in.Expect.Payable = 999999, 0
	}, httpx.CodeQuoteChanged)
	tamper("pq-stale-asof", func(in *CreateRenewalInput) { in.Expect.AsOf = time.Now().Add(-11 * time.Minute) }, httpx.CodeQuoteChanged)
	tamper("pq-future-asof", func(in *CreateRenewalInput) { in.Expect.AsOf = time.Now().Add(time.Minute) }, httpx.CodeQuoteChanged)
	tamper("pq-foreign-price", func(in *CreateRenewalInput) { in.PriceID = priceAdv }, httpx.CodeConflict)
	tamper("pq-foreign-sub", func(in *CreateRenewalInput) { in.SubscriptionID = s4 }, httpx.CodeNotFound)
	_, err = p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u9, PlanID: planBasic, PriceID: priceStd,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u9, CheckoutIdempotencyScope, "pq-price-of-other-plan")})
	wantCode("price of another plan", err, httpx.CodeNotFound)
	_, err = p.billing.CreateTrafficPackOrder(ctx, p.fx.tenant, CreateTrafficPackOrderInput{UserID: u9,
		SubscriptionID: s4, PackID: packID,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u9, CheckoutIdempotencyScope, "pq-pack-foreign-sub")})
	wantCode("pack on another user's subscription", err, httpx.CodeNotFound)
	// 原样的报价仍然能确认：时间只过了几毫秒，在窗口内
	mustOrder(t, "pq-tamper-ok")(p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
		UserID: u9, SubscriptionID: s9, UseBalance: 1000, Expect: expect(tq, q, true),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u9, RenewalIdempotencyScope, "pq-tamper-ok")}))
	t.Log("marker=purchase_quote_pg18_tamper_rejected_ok")

	// 5) 价格漂移：后台改价；原订阅又用了流量（剩余价值按当前用量算）
	u10 := newUser("drift")
	s10 := buy(u10, planStd, priceStd, "pq-drift-buy", 3000)
	dq := quote(QuoteInput{UserID: u10, Action: QuoteChange, SubscriptionID: s10, PlanID: planAdv})
	q = dq.Quotes[0]
	p.report(s10, 300_000, 300_000)
	_, err = p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{UserID: u10, SubscriptionID: s10,
		PlanID: planAdv, PriceID: priceAdv, Expect: expect(dq, q, false),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u10, PlanChangeIdempotencyScope, "pq-drift-traffic")})
	wantCode("traffic used after the quote", err, httpx.CodeQuoteChanged)
	// 价格档不可改（改价是新建一档），这里用余额变化：报价后又进了一笔余额，用余额的那组数就变了
	orderReleasePG18FundBalance(t, ctx, p.app, p.fx.tenant, u10, 500)
	rq = quote(QuoteInput{UserID: u10, Action: QuoteRenew, SubscriptionID: s10})
	orderReleasePG18FundBalance(t, ctx, p.app, p.fx.tenant, u10, 700)
	_, err = p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{UserID: u10, SubscriptionID: s10,
		UseBalance: 1200, Expect: expect(rq, rq.Quotes[0], true),
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u10, RenewalIdempotencyScope, "pq-drift-balance")})
	wantCode("balance changed after the quote", err, httpx.CodeQuoteChanged)
	t.Log("marker=purchase_quote_pg18_drift_ok")

	// 6) 过期 30 天内换不同款：剩余价值为 0、链接不变；窗口关闭后回 409
	u11 := newUser("revive")
	s11 := buy(u11, planStd, priceStd, "pq-revive-buy", 3000)
	link11 := link(s11)
	p.travel(s11, 24*40)
	p.must(`UPDATE subscriptions SET status='expired' WHERE id=$1::uuid`, s11)
	revive, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{UserID: u11, SubscriptionID: s11,
		PlanID: planBasic, PriceID: priceBasic,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u11, PlanChangeIdempotencyScope, "pq-revive")})
	if err != nil || revive.ProrationCredit != 0 || revive.PayableAmount != 1500 {
		t.Fatalf("revive change=%+v err=%v", revive, err)
	}
	p.pay("pq-revive-pay", revive.OrderID, 1500)
	if link(s11) != link11 || scalar(`SELECT count(*) FROM subscriptions WHERE id=$1::uuid AND status='active'
		AND plan_id=$2::uuid`, s11, planBasic) != 1 {
		t.Fatal("reviving with another plan must keep the link")
	}
	u12 := newUser("closed")
	s12 := buy(u12, planStd, priceStd, "pq-closed-buy", 3000)
	p.must(`UPDATE subscriptions SET status='expired', renewal_closed_at=now() WHERE id=$1::uuid`, s12)
	_, err = p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{UserID: u12, SubscriptionID: s12,
		PlanID: planBasic, PriceID: priceBasic,
		Claim: orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u12, PlanChangeIdempotencyScope, "pq-closed")})
	wantCode("closed window", err, httpx.CodeConflict)
	t.Log("marker=purchase_quote_pg18_revive_ok")
}
