package billing

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkExpiryRulesPG18 是 TestSubscriptionPeriodPG18 的子测试（sub_period 域），覆盖用户
// 2026-10-07 定的到期与续费规则：
//
//  1. 生效 → 过期扫描 → 过期期间拉到提示节点、链接只读不可换 → 续费恢复：链接相同、
//     四种配额对齐清零、续费事件起始状态 expired；
//  2. 过期 29 天仍可原地续费；31 天关窗：凭据吊销、拉取 404、续费拒绝、可以新购；
//  3. 提前续费：新周期接在原到期日之后，本期已用量不动，到点滚进新周期；
//  4. 礼品卡与后台加时长救回：状态回 active，流量按天数折算、已用量沿用；
//  5. 同套餐：门户新购拒绝、后台人工开单与套餐卡都在原订阅上续费。
func checkExpiryRulesPG18(t *testing.T, p *subPeriodPG18, conn *pgx.Conn) {
	ctx := p.ctx
	// 带全部四种周期配额的月付套餐
	product, plan, version, price := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	p.must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,$3,$3,'active')`,
		product, p.fx.tenant, "sp-expiry-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,allow_upgrade)
		VALUES($1,$2,$3,$4,$4,'draft',true)`, plan, p.fx.tenant, product, "sp-expiry-plan-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`, version, p.fx.tenant, plan)
	p.must(`INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period)
		VALUES($1,$2,'traffic.bytes',1000,'bytes','cycle'),($1,$2,'traffic.bytes',5000,'bytes','total'),
		      ($1,$2,'traffic.bytes',800,'bytes','month'),($1,$2,'traffic.bytes',300,'bytes','day')`,
		p.fx.tenant, version)
	p.must(`UPDATE plan_versions SET frozen_at=now(),status='published' WHERE id=$1`, version)
	p.must(`UPDATE plans SET current_version_id=$2,status='active' WHERE id=$1`, plan, version)
	p.must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,
		interval_count,status) VALUES($1,$2,$3,'CNY',1000,'month',1,'active')`, price, p.fx.tenant, product)

	newUser := func(label string) string {
		id := uuid.NewString()
		p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,$4,'active')`,
			id, p.fx.tenant, "exp-"+label+"-"+id[:8]+"@example.test", "Expiry "+label)
		return id
	}
	buy := func(user, label string) string {
		claim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, user, CheckoutIdempotencyScope, label)
		order, err := p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
			UserID: user, PlanID: plan, PriceID: price, Claim: claim,
		})
		if err != nil {
			t.Fatalf("buy %s: %v", label, err)
		}
		return p.pay(label, order.OrderID, 1000).SubscriptionID
	}
	renew := func(user, sub, label string) *CreateOrderOutput {
		claim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, user, RenewalIdempotencyScope, label)
		return mustOrder(t, label)(p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
			UserID: user, SubscriptionID: sub, Claim: claim,
		}))
	}
	// lapse 把这条订阅的全部时间点往前挪，让它在 ago 之前到期
	lapse := func(sub string, ago time.Duration) {
		var end time.Time
		if err := p.admin.QueryRow(ctx, `SELECT current_period_end FROM subscriptions WHERE id=$1::uuid`,
			sub).Scan(&end); err != nil {
			t.Fatal(err)
		}
		p.travel(sub, int(math.Ceil((time.Until(end) + ago).Hours())))
	}
	scan := func(step string) ExpiryScanResult {
		res, err := p.billing.ScanExpiredSubscriptions(ctx, p.fx.tenant)
		if err != nil {
			t.Fatalf("%s: expiry scan: %v", step, err)
		}
		return res
	}
	status := func(sub string) (st string, closed bool) {
		if err := p.admin.QueryRow(ctx, `SELECT status, renewal_closed_at IS NOT NULL FROM subscriptions
			WHERE id=$1::uuid`, sub).Scan(&st, &closed); err != nil {
			t.Fatal(err)
		}
		return
	}

	// 1) 生效 → 过期 → 续费恢复
	u1 := newUser("restore")
	s1 := buy(u1, "exp-restore-buy")
	token := p.rotate(u1, s1)
	var credBefore string
	if err := p.admin.QueryRow(ctx, `SELECT id::text FROM subscription_credentials
		WHERE subscription_id=$1::uuid AND status='active'`, s1).Scan(&credBefore); err != nil {
		t.Fatal(err)
	}
	p.must(`UPDATE quota_balances SET consumed = 900 WHERE subscription_id=$1::uuid AND period <> 'total'`, s1)
	p.must(`UPDATE quota_balances SET consumed = 4900 WHERE subscription_id=$1::uuid AND period = 'total'`, s1)
	lapse(s1, 2*time.Hour)
	if res := scan("first scan"); res.Expired < 1 {
		t.Fatalf("expiry scan flipped %+v", res)
	}
	if st, closed := status(s1); st != "expired" || closed {
		t.Fatalf("after scan status=%s closed=%v", st, closed)
	}
	var expiredEvents int
	if err := p.admin.QueryRow(ctx, `SELECT count(*) FROM subscription_events WHERE subscription_id=$1::uuid
		AND event_type='expired' AND from_status='active' AND to_status='expired' AND actor_kind='scheduler'`,
		s1).Scan(&expiredEvents); err != nil || expiredEvents != 1 {
		t.Fatalf("expired events=%d err=%v", expiredEvents, err)
	}
	if res := scan("rescan"); res.Expired != 0 || res.Closed != 0 {
		t.Fatalf("second expiry scan changed rows: %+v", res)
	}
	// 过期期间：令牌有效回提示（不带节点），乱写的令牌仍是 404；链接只读列出、不许换
	pulled, err := p.pull(token)
	if err != nil || pulled.Expired == nil || len(pulled.Nodes) != 0 || pulled.Usage.Expire > time.Now().Unix() {
		t.Fatalf("expired pull=%+v err=%v", pulled, err)
	}
	if _, err := p.pull(strings.Repeat("x", 43)); !errors.Is(err, subscription.ErrNotFound) {
		t.Fatalf("invalid token err=%v, want ErrNotFound", err)
	}
	links, err := p.subs.ListLinks(ctx, p.fx.tenant, u1)
	if err != nil || len(links) != 1 || links[0].Token != token || !links[0].Expired {
		t.Fatalf("expired links=%+v err=%v", links, err)
	}
	if _, err := p.subs.Rotate(ctx, p.fx.tenant, u1, s1); !errors.Is(err, subscription.ErrRotateWhileExpired) {
		t.Fatalf("rotate while expired err=%v", err)
	}
	t.Log("marker=expiry_pg18_expired_notice_ok")

	order := renew(u1, s1, "exp-restore-renew")
	p.pay("exp-restore-renew", order.OrderID, order.PayableAmount)
	if st, _ := status(s1); st != "active" {
		t.Fatalf("renewed status=%s", st)
	}
	end := p.aligned("renewal after expiry", s1)
	var (
		start                      time.Time
		zeroed, aligned, credsSame bool
		dayOK, monthOK, totalOK    bool
		renewedFrom                string
		resetLogs                  int
	)
	if err := p.admin.QueryRow(ctx, `
		SELECT s.current_period_start,
		       bool_and(q.consumed = 0), bool_and(q.period_start = s.current_period_start),
		       bool_and(q.period_end = q.period_start + interval '1 day') FILTER (WHERE q.period = 'day'),
		       bool_and(q.period_end = q.period_start + interval '1 month') FILTER (WHERE q.period = 'month'),
		       bool_and(q.period_end IS NULL) FILTER (WHERE q.period = 'total'),
		       EXISTS (SELECT 1 FROM subscription_credentials c WHERE c.subscription_id = s.id
		                 AND c.status = 'active' AND c.id = $2::uuid),
		       (SELECT e.from_status FROM subscription_events e WHERE e.subscription_id = s.id
		         AND e.event_type = 'renewed' ORDER BY e.id DESC LIMIT 1),
		       (SELECT count(*) FROM traffic_reset_logs l WHERE l.subscription_id = s.id
		         AND l.reason = 'renewal')
		  FROM subscriptions s JOIN quota_balances q ON q.subscription_id = s.id
		 WHERE s.id = $1::uuid
		 GROUP BY s.id, s.current_period_start`, s1, credBefore).Scan(&start, &zeroed, &aligned,
		&dayOK, &monthOK, &totalOK, &credsSame, &renewedFrom, &resetLogs); err != nil {
		t.Fatal(err)
	}
	if !zeroed || !aligned || !dayOK || !monthOK || !totalOK || !credsSame || renewedFrom != "expired" ||
		resetLogs != 4 || time.Since(start) > time.Minute || end.Before(start.AddDate(0, 0, 28)) {
		t.Fatalf("restore: start=%s end=%s zeroed=%v aligned=%v day=%v month=%v total=%v same cred=%v from=%s logs=%d",
			start, end, zeroed, aligned, dayOK, monthOK, totalOK, credsSame, renewedFrom, resetLogs)
	}
	if pulled, err := p.pull(token); err != nil || pulled.Expired != nil {
		t.Fatalf("pull after renewal=%+v err=%v", pulled, err)
	}
	t.Log("marker=expiry_pg18_restore_same_link_ok")

	// 2) 29 天与 31 天的分界
	u2, u3 := newUser("d29"), newUser("d31")
	s2, s3 := buy(u2, "exp-d29-buy"), buy(u3, "exp-d31-buy")
	token3 := p.rotate(u3, s3)
	lapse(s2, 29*24*time.Hour)
	lapse(s3, 31*24*time.Hour)
	res := scan("window scan")
	if res.Expired < 2 || res.Closed < 1 || res.RevokedCredentials < 1 {
		t.Fatalf("window scan=%+v", res)
	}
	if st, closed := status(s2); st != "expired" || closed {
		t.Fatalf("29 days: status=%s closed=%v", st, closed)
	}
	if st, closed := status(s3); st != "expired" || !closed {
		t.Fatalf("31 days: status=%s closed=%v", st, closed)
	}
	order = renew(u2, s2, "exp-d29-renew")
	p.pay("exp-d29-renew", order.OrderID, order.PayableAmount)
	if st, _ := status(s2); st != "active" {
		t.Fatalf("29 days renewal status=%s", st)
	}
	claim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u3, RenewalIdempotencyScope, "exp-d31-renew")
	_, err = p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{UserID: u3, SubscriptionID: s3, Claim: claim})
	wantHTTPCode(t, "31 days renewal", err, httpx.CodeConflict)
	if _, err := p.pull(token3); !errors.Is(err, subscription.ErrNotFound) {
		t.Fatalf("31 days pull err=%v, want 404 after the window closed", err)
	}
	claim = orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u3, CheckoutIdempotencyScope, "exp-d31-new")
	if _, err := p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u3, PlanID: plan,
		PriceID: price, Claim: claim, RejectSamePlan: true}); err != nil {
		t.Fatalf("new purchase after the window closed: %v", err)
	}
	t.Log("marker=expiry_pg18_window_29_31_ok")

	// 3) 提前续费：本期不动，到原到期日滚进新周期
	u4 := newUser("early")
	s4 := buy(u4, "exp-early-buy")
	p.must(`UPDATE quota_balances SET consumed = 400 WHERE subscription_id=$1::uuid AND period = 'cycle'`, s4)
	oldEnd := p.aligned("before early renewal", s4)
	order = renew(u4, s4, "exp-early-renew")
	p.pay("exp-early-renew", order.OrderID, order.PayableAmount)
	var newEnd, cycleEnd, cycleStart time.Time
	var cycleConsumed int64
	readCycle := func() {
		if err := p.admin.QueryRow(ctx, `SELECT s.current_period_end, q.period_start, q.period_end, q.consumed
			FROM subscriptions s JOIN quota_balances q ON q.subscription_id = s.id AND q.period = 'cycle'
			WHERE s.id = $1::uuid`, s4).Scan(&newEnd, &cycleStart, &cycleEnd, &cycleConsumed); err != nil {
			t.Fatal(err)
		}
	}
	readCycle()
	if !cycleEnd.Equal(oldEnd) || cycleConsumed != 400 || !newEnd.Equal(oldEnd.AddDate(0, 1, 0)) {
		t.Fatalf("early renewal: end=%s cycle=[%s,%s] consumed=%d old end=%s",
			newEnd, cycleStart, cycleEnd, cycleConsumed, oldEnd)
	}
	// 走到原到期日之后：cycle 行到点，订阅周期末还在一个月后
	p.travel(s4, int(math.Ceil(time.Until(oldEnd).Hours()))+1)
	if n, err := p.billing.RollQuotaPeriods(ctx, p.fx.tenant); err != nil || n < 1 {
		t.Fatalf("roll after the old end n=%d err=%v", n, err)
	}
	readCycle()
	if cycleConsumed != 0 || !cycleEnd.Equal(newEnd) || !cycleStart.Before(time.Now()) {
		t.Fatalf("rolled cycle=[%s,%s] consumed=%d sub end=%s", cycleStart, cycleEnd, cycleConsumed, newEnd)
	}
	t.Log("marker=expiry_pg18_early_renewal_ok")

	// 4) 救回：礼品卡 3 天、后台 7 天；流量按天数折算，已用量沿用
	prorate := func(days int) (lo, hi int64) {
		return 1000 * int64(days) / 31, 1000 * int64(days) / 28
	}
	rescued := func(step, sub string, days int, consumed int64) {
		var st string
		var limit, used int64
		var dayZero bool
		if err := p.admin.QueryRow(ctx, `
			SELECT s.status, q.limit_value, q.consumed,
			       (SELECT bool_and(d.consumed = 0 AND d.period_start > now() - interval '1 minute')
			          FROM quota_balances d WHERE d.subscription_id = s.id AND d.period IN ('day','month'))
			  FROM subscriptions s JOIN quota_balances q ON q.subscription_id = s.id AND q.period = 'cycle'
			 WHERE s.id = $1::uuid`, sub).Scan(&st, &limit, &used, &dayZero); err != nil {
			t.Fatal(err)
		}
		lo, hi := prorate(days)
		if st != "active" || used != consumed || limit < 1000+lo || limit > 1000+hi || !dayZero {
			t.Fatalf("%s: status=%s limit=%d consumed=%d day/month aligned=%v", step, st, limit, used, dayZero)
		}
		p.aligned(step, sub)
	}
	u5, u6 := newUser("gift"), newUser("admin")
	s5, s6 := buy(u5, "exp-gift-buy"), buy(u6, "exp-admin-buy")
	p.must(`UPDATE quota_balances SET consumed = 1000 WHERE subscription_id = ANY($1::uuid[])`, []string{s5, s6})
	lapse(s5, 48*time.Hour)
	lapse(s6, 48*time.Hour)
	scan("rescue scan")
	orderReleasePG18InTxAs(t, ctx, p.app, p.fx.tenant, u5, func(tx pgx.Tx) error {
		return p.billing.GiftGranter().ExtendExpiry(ctx, tx, p.fx.tenant, u5, 3)
	})
	rescued("gift card rescue", s5, 3, 1000)
	extendClaim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, p.fx.referrer,
		SubscriptionExtendIdempotencyScope, "exp-admin-extend")
	if _, err := p.billing.ExtendSubscriptionAsAdmin(ctx, p.fx.tenant, AdminExtendInput{
		SubscriptionID: s6, ActorID: p.fx.referrer, Days: 7, Reason: "救回过期订阅测试", Claim: extendClaim,
	}); err != nil {
		t.Fatalf("admin rescue: %v", err)
	}
	rescued("admin rescue", s6, 7, 1000)
	var rescueEvents int
	if err := p.admin.QueryRow(ctx, `SELECT count(*) FROM subscription_events
		WHERE subscription_id = ANY($1::uuid[]) AND event_type='extended' AND from_status='expired'
		  AND to_status='active' AND (payload->>'restart')::boolean`, []string{s5, s6}).Scan(&rescueEvents); err != nil ||
		rescueEvents != 2 {
		t.Fatalf("rescue events=%d err=%v", rescueEvents, err)
	}
	t.Log("marker=expiry_pg18_rescue_prorated_ok")

	// 5) 同套餐只续不新开
	u7 := newUser("same")
	s7 := buy(u7, "exp-same-buy")
	claim = orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, u7, CheckoutIdempotencyScope, "exp-same-new")
	_, err = p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{UserID: u7, PlanID: plan, PriceID: price,
		Claim: claim, RejectSamePlan: true})
	wantHTTPCode(t, "same plan portal purchase", err, httpx.CodeConflict)
	manualClaim := orderReleasePG18Claim(t, ctx, conn, p.fx.tenant, p.fx.referrer, CheckoutIdempotencyScope, "exp-same-manual")
	manual := mustOrder(t, "same plan manual order")(p.billing.CreateManualOrder(ctx, p.fx.tenant, CreateManualOrderInput{
		UserID: u7, PlanID: plan, PriceID: price, Reason: "同套餐人工开单测试", ActorID: p.fx.referrer,
		Claim: manualClaim,
	}))
	var kind, orderSub string
	var subs, manualAudits int
	if err := p.admin.QueryRow(ctx, `
		SELECT o.kind, o.subscription_id::text,
		       (SELECT count(*) FROM subscriptions s WHERE s.user_id = o.user_id),
		       (SELECT count(*) FROM audit_events a WHERE a.action = 'order.manual_created'
		          AND a.resource_id = o.id AND (a.after_digest->>'renewal')::boolean)
		  FROM orders o WHERE o.id = $1::uuid`, manual.OrderID).Scan(&kind, &orderSub, &subs, &manualAudits); err != nil ||
		kind != "renewal" || orderSub != s7 || subs != 1 || manual.Status != "fulfilled" || manualAudits != 1 {
		t.Fatalf("manual same plan kind=%s sub=%s subs=%d status=%s audits=%d err=%v",
			kind, orderSub, subs, manual.Status, manualAudits, err)
	}
	subEnd := func(sub string) time.Time {
		var end time.Time
		if err := p.admin.QueryRow(ctx, `SELECT current_period_end FROM subscriptions WHERE id=$1::uuid`,
			sub).Scan(&end); err != nil {
			t.Fatal(err)
		}
		return end
	}
	beforeCard := subEnd(s7)
	var cardSub, mode string
	orderReleasePG18InTxAs(t, ctx, p.app, p.fx.tenant, u7, func(tx pgx.Tx) error {
		var err error
		cardSub, mode, _, _, err = p.billing.GiftGranter().GrantPlan(ctx, tx, p.fx.tenant, u7, "", plan, price, "同套餐套餐卡")
		return err
	})
	if cardSub != s7 || mode != PlanGrantRenewed || !subEnd(s7).Equal(beforeCard.AddDate(0, 1, 0)) {
		t.Fatalf("plan card sub=%s mode=%s", cardSub, mode)
	}
	t.Log("marker=expiry_pg18_same_plan_renews_ok")
}

// mustOrder 把 (订单, err) 收成订单，出错即失败。
func mustOrder(t *testing.T, step string) func(*CreateOrderOutput, error) *CreateOrderOutput {
	return func(out *CreateOrderOutput, err error) *CreateOrderOutput {
		t.Helper()
		if err != nil || out == nil {
			t.Fatalf("%s: out=%+v err=%v", step, out, err)
		}
		return out
	}
}
