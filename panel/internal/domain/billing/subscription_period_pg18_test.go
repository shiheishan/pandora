package billing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// subPeriodPG18 是 TestSubscriptionPeriodPG18 共用的夹具与被测服务。
type subPeriodPG18 struct {
	t       *testing.T
	ctx     context.Context
	admin   *pgxpool.Pool
	app     *platformdb.Pool
	fx      orderReleasePG18Fixture
	billing *Service
	subs    *subscription.Service
	nodes   *nodefabric.Service
	prefix  string
	nodeID  string
}

func (p *subPeriodPG18) must(sql string, args ...any) {
	p.t.Helper()
	if _, err := p.admin.Exec(p.ctx, sql, args...); err != nil {
		p.t.Fatalf("fixture: %v\nSQL: %s", err, sql)
	}
}

// seedPlan 建一个已发布、可购、可变更的月付套餐，带 cycle 流量配额。
func (p *subPeriodPG18) seedPlan(code string, price, traffic int64) (string, string) {
	p.t.Helper()
	product, plan, version, priceID := uuid.NewString(), uuid.NewString(),
		uuid.NewString(), uuid.NewString()
	p.must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($1,$2,$3,$3,'active')`,
		product, p.fx.tenant, code+"-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,allow_upgrade)
		VALUES($1,$2,$3,$4,$4,'draft',true)`, plan, p.fx.tenant, product, code+"-plan-"+p.fx.suffix[:8])
	p.must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`,
		version, p.fx.tenant, plan)
	p.must(`INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period)
		VALUES($1,$2,'traffic.bytes',$3,'bytes','cycle')`, p.fx.tenant, version, traffic)
	p.must(`UPDATE plan_versions SET frozen_at=now(),status='published' WHERE id=$1`, version)
	p.must(`UPDATE plans SET current_version_id=$2,status='active' WHERE id=$1`, plan, version)
	p.must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,
		interval_count,status) VALUES($1,$2,$3,'CNY',$4,'month',1,'active')`,
		priceID, p.fx.tenant, product, price)
	return plan, priceID
}

func (p *subPeriodPG18) pay(label, orderID string, amount int64) *PaymentWebhookOutput {
	p.t.Helper()
	out, err := p.billing.HandlePaymentWebhook(p.ctx, p.fx.tenant, PaymentWebhookInput{
		ProviderCode: p.fx.providerCode, ProviderEventID: label + "-event-" + p.fx.suffix,
		ProviderPaymentID: label + "-payment-" + p.fx.suffix, EventType: "payment.succeeded",
		OrderID: orderID, Amount: amount, Currency: "CNY",
		RawPayload: map[string]any{"fixture": "subscription-period"}, SignatureVerified: true,
	})
	if err != nil || out == nil || !out.Processed {
		p.t.Fatalf("settle %s output=%#v err=%v", label, out, err)
	}
	return out
}

// aligned 断言订阅周期末、全部 active 凭据的到期、本周期 cycle 配额行的周期末三者一致，
// 返回周期末。
func (p *subPeriodPG18) aligned(step, subID string) time.Time {
	p.t.Helper()
	var (
		end                           time.Time
		creds, credOff, cycle, cycOff int
	)
	if err := p.admin.QueryRow(p.ctx, `
		SELECT s.current_period_end,
		       (SELECT count(*) FROM subscription_credentials c
		         WHERE c.subscription_id = s.id AND c.status = 'active'),
		       (SELECT count(*) FROM subscription_credentials c
		         WHERE c.subscription_id = s.id AND c.status = 'active'
		           AND c.expires_at IS DISTINCT FROM s.current_period_end),
		       (SELECT count(*) FROM quota_balances q
		         WHERE q.subscription_id = s.id AND q.period = 'cycle'),
		       (SELECT count(*) FROM quota_balances q
		         WHERE q.subscription_id = s.id AND q.period = 'cycle'
		           AND q.period_end IS DISTINCT FROM s.current_period_end)
		  FROM subscriptions s WHERE s.id = $1::uuid`, subID).Scan(
		&end, &creds, &credOff, &cycle, &cycOff); err != nil {
		p.t.Fatalf("%s: read period alignment: %v", step, err)
	}
	if creds < 1 || credOff != 0 || cycle < 1 || cycOff != 0 {
		p.t.Fatalf("%s: period end %s; active credentials=%d (%d off), cycle rows=%d (%d off)",
			step, end, creds, credOff, cycle, cycOff)
	}
	return end
}

// setPeriodEnd 把订阅、本周期 cycle 行与 active 凭据的周期末一起设成 end（一致的初始状态）。
func (p *subPeriodPG18) setPeriodEnd(subID string, end time.Time) {
	p.t.Helper()
	p.must(`UPDATE subscriptions SET current_period_end=$2 WHERE id=$1::uuid`, subID, end)
	p.must(`UPDATE quota_balances SET period_end=$2 WHERE subscription_id=$1::uuid AND period='cycle'`, subID, end)
	p.must(`UPDATE subscription_credentials SET expires_at=$2 WHERE subscription_id=$1::uuid AND status='active'`, subID, end)
}

// travel 模拟时间流逝：把这条订阅的全部时间点一起往前挪，「现在」就相当于往后走了 d。
func (p *subPeriodPG18) travel(subID string, hours int) {
	p.t.Helper()
	p.must(`UPDATE subscriptions SET current_period_start=current_period_start-make_interval(hours => $2),
		current_period_end=current_period_end-make_interval(hours => $2) WHERE id=$1::uuid`, subID, hours)
	p.must(`UPDATE quota_balances SET period_start=period_start-make_interval(hours => $2),
		period_end=period_end-make_interval(hours => $2) WHERE subscription_id=$1::uuid`, subID, hours)
	p.must(`UPDATE subscription_credentials SET expires_at=expires_at-make_interval(hours => $2)
		WHERE subscription_id=$1::uuid`, subID, hours)
}

// snapshot 是本租户凭据到期、配额行与订阅周期末的指纹，exclude 那条订阅不计。
func (p *subPeriodPG18) snapshot(exclude string) string {
	p.t.Helper()
	var s string
	if err := p.admin.QueryRow(p.ctx, `
		SELECT string_agg(x, ',' ORDER BY x) FROM (
			SELECT c.id::text || ':' || c.status || ':' || coalesce(c.expires_at::text, '') AS x
			  FROM subscription_credentials c WHERE c.tenant_id = $1 AND c.subscription_id <> $2::uuid
			UNION ALL
			SELECT q.id::text || ':' || q.period_start::text || ':' || coalesce(q.period_end::text, '')
			       || ':' || q.consumed
			  FROM quota_balances q WHERE q.tenant_id = $1 AND q.subscription_id <> $2::uuid
			UNION ALL
			SELECT s.id::text || ':' || s.status || ':' || coalesce(s.current_period_end::text, '')
			  FROM subscriptions s WHERE s.tenant_id = $1 AND s.id <> $2::uuid) t`,
		p.fx.tenant, exclude).Scan(&s); err != nil {
		p.t.Fatalf("snapshot: %v", err)
	}
	return s
}

func (p *subPeriodPG18) pull(token string) (*subscription.Pull, error) {
	return p.subs.LoadPull(p.ctx, p.fx.tenant, p.prefix, token)
}

func (p *subPeriodPG18) cycleConsumed(subID string) int64 {
	p.t.Helper()
	var consumed int64
	if err := p.admin.QueryRow(p.ctx, `SELECT consumed FROM quota_balances
		WHERE subscription_id=$1::uuid AND metric='traffic.bytes' AND period='cycle'`,
		subID).Scan(&consumed); err != nil {
		p.t.Fatalf("read cycle consumed: %v", err)
	}
	return consumed
}

// report 让节点上报这条订阅的一笔用量（上下行合计 up+down）。
func (p *subPeriodPG18) report(subID string, up, down int64) {
	p.t.Helper()
	var uid int64
	if err := p.admin.QueryRow(p.ctx, `SELECT node_uid FROM subscriptions WHERE id=$1::uuid`,
		subID).Scan(&uid); err != nil {
		p.t.Fatalf("read node uid: %v", err)
	}
	raw := `{"` + strconv.FormatInt(uid, 10) + `":[` + strconv.FormatInt(up, 10) + `,` +
		strconv.FormatInt(down, 10) + `]}`
	res, err := p.nodes.ReportTraffic(p.ctx, p.fx.tenant,
		&nodefabric.ServingNode{ID: p.nodeID, TrafficRate: 1}, []byte(raw))
	if err != nil || res.Duplicate {
		p.t.Fatalf("report traffic %s: %+v %v", raw, res, err)
	}
}

func (p *subPeriodPG18) rotate(userID, subID string) string {
	p.t.Helper()
	token, err := p.subs.Rotate(p.ctx, p.fx.tenant, userID, subID)
	if err != nil {
		p.t.Fatalf("rotate subscription link: %v", err)
	}
	return token
}

// runMigrationUp 以迁移角色执行 00102 的 Up 段。
func (p *subPeriodPG18) runMigrationUp() {
	p.t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00102_subscription_period_resync.sql"))
	if err != nil {
		p.t.Fatalf("read migration: %v", err)
	}
	src := string(raw)
	up, down := strings.Index(src, "-- +goose Up"), strings.Index(src, "-- +goose Down")
	if up < 0 || down < up {
		p.t.Fatal("migration 00102 is missing goose Up/Down markers")
	}
	// 简单查询协议：多条语句在同一个隐式事务里执行，SET LOCAL 照常生效
	if _, err := p.admin.Exec(p.ctx, src[up:down], pgx.QueryExecModeSimpleProtocol); err != nil {
		p.t.Fatalf("run migration 00102 up: %v", err)
	}
}

func wantHTTPCode(t *testing.T, step string, err error, code httpx.Code) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != code {
		t.Fatalf("%s err=%v want %s", step, err, code)
	}
}

// TestSubscriptionPeriodPG18 证明订阅周期末、凭据到期与本周期配额一起走（第 2 波规划第 3、5 条）：
//   - 礼品卡延期后过了原到期日：订阅拉取照常、门户链接还在、流量照常计入本周期；
//   - 旧写法留下的分叉（只推了订阅一行）由 00102 拉齐，重跑不再改动，已吊销凭据不动；
//   - 后台加时长同样三处一起改，写审计、订阅事件，并在同一事务里完成幂等记录；
//   - 续费、变更套餐之后三处也一致（四条路径同一个凭据写入点）；
//   - 非生效订阅、没有到期时间的订阅、不存在的订阅被拒绝，什么都不改。
//
// 由 run-pg18-gates.sh 的 sub_period 域驱动。
func TestSubscriptionPeriodPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUB_PERIOD", DatabasePrefix: "pandora_sub_period_gate",
		MarkerTable: "pandora_sub_period_test_marker", CommentTag: "pandora-sub-period-pg18",
	})
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire fixture connection: %v", err)
	}
	defer conn.Release()
	orderReleasePG18AssertRuntimeTarget(t, ctx, app, conn.Conn())

	envelope, err := crypto.NewEnvelope([]byte("sub-period-pg18-envelope-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	p := &subPeriodPG18{
		t: t, ctx: ctx, admin: admin, app: app,
		fx:      orderReleasePG18Seed(t, ctx, conn.Conn()),
		billing: NewService(app, nil),
		subs:    subscription.New(app, []byte("sub-period-pg18-salt"), envelope),
		nodes:   nodefabric.NewService(app, nil),
		nodeID:  uuid.NewString(),
	}
	p.prefix = "sp" + p.fx.suffix[:10]
	p.must(`UPDATE tenants SET sub_path_prefix=$2 WHERE id=$1`, p.fx.tenant, p.prefix)
	p.must(`INSERT INTO nodes (id, tenant_id, name, status) VALUES ($1, $2, 'sub-period-node', 'active')`,
		p.nodeID, p.fx.tenant)
	planA, priceA := p.seedPlan("sp-basic", 1000, 1_000_000)
	planB, priceB := p.seedPlan("sp-pro", 3000, 1_000_000)
	buyer, control, operator := p.fx.buyer, p.fx.commissionBuyer, p.fx.referrer

	// 1) 买基础版，换发一次链接拿到令牌明文（开通时签发的那条明文不回给调用方）。
	claim := orderReleasePG18Claim(t, ctx, conn.Conn(), p.fx.tenant, buyer, CheckoutIdempotencyScope, "sp-buy")
	bought, err := p.billing.CreateOrder(ctx, p.fx.tenant, CreateOrderInput{
		UserID: buyer, PlanID: planA, PriceID: priceA, Claim: claim,
	})
	if err != nil {
		t.Fatalf("buy plan A: %v", err)
	}
	subID := p.pay("sp-buy", bought.OrderID, 1000).SubscriptionID
	token := p.rotate(buyer, subID)
	p.aligned("purchase", subID)

	// 2) 原到期日在一天后；礼品卡送 30 天：三处一起推到原到期日 + 30 天。
	origEnd := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	p.setPeriodEnd(subID, origEnd)
	orderReleasePG18InTxAs(t, ctx, app, p.fx.tenant, buyer, func(tx pgx.Tx) error {
		return p.billing.GiftGranter().ExtendExpiry(ctx, tx, p.fx.tenant, buyer, "", 30)
	})
	if end := p.aligned("gift card extension", subID); !end.Equal(origEnd.AddDate(0, 0, 30)) {
		t.Fatalf("gift card extension end=%s want %s", end, origEnd.AddDate(0, 0, 30))
	}
	var giftEvents int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM subscription_events
		WHERE subscription_id=$1::uuid AND event_type='extended' AND actor_kind='user'
		  AND actor_id=$2::uuid AND payload->>'source'='gift_card' AND (payload->>'days')::int=30
		  AND (payload->>'credentials')::int=1 AND (payload->>'cycle_quota_rows')::int=1`,
		subID, buyer).Scan(&giftEvents); err != nil || giftEvents != 1 {
		t.Fatalf("gift card extension events=%d err=%v", giftEvents, err)
	}

	// 3) 两天过去，原到期日已过：拉取照常、门户链接还在、流量记到本周期。
	p.travel(subID, 48)
	end := p.aligned("after the original end", subID)
	pulled, err := p.pull(token)
	if err != nil || pulled.Usage.Expire != end.Unix() {
		t.Fatalf("pull after the original end: usage=%+v err=%v", pulled, err)
	}
	links, err := p.subs.ListLinks(ctx, p.fx.tenant, buyer)
	if err != nil || len(links) != 1 || links[0].SubscriptionID != subID || links[0].Token != token ||
		links[0].ExpiresAt == nil || !links[0].ExpiresAt.Equal(end) {
		t.Fatalf("portal links after the original end=%+v err=%v", links, err)
	}
	before := p.cycleConsumed(subID)
	p.report(subID, 100, 23)
	if got := p.cycleConsumed(subID); got != before+123 {
		t.Fatalf("traffic after the original end consumed=%d want %d", got, before+123)
	}
	t.Log("marker=sub_period_pg18_gift_extension_survives_original_end_ok")

	// 4) 对照：按旧写法只推订阅一行，过了原到期日就 404、流量不计；00102 把它拉齐。
	var controlSub string
	orderReleasePG18InTxAs(t, ctx, app, p.fx.tenant, control, func(tx pgx.Tx) error {
		var err error
		controlSub, _, _, _, err = p.billing.GiftGranter().GrantPlan(ctx, tx, p.fx.tenant, control, "", planA, priceA, purchase.Choice{})
		return err
	})
	controlToken := p.rotate(control, controlSub)
	p.setPeriodEnd(controlSub, origEnd)
	p.must(`UPDATE subscriptions SET current_period_end=current_period_end+interval '30 days' WHERE id=$1::uuid`, controlSub)
	p.must(`UPDATE subscription_credentials SET expires_at=now()-interval '5 days'
		WHERE subscription_id=$1::uuid AND status='revoked'`, controlSub)
	p.travel(controlSub, 48)
	// 旧写法的分叉：凭据过了旧到期日。原先回 404；现在令牌有效、按「已过期」回提示节点
	// （w5expiry 规则 1），同样拿不到任何真实节点
	if pulled, err := p.pull(controlToken); err != nil || pulled.Expired == nil || len(pulled.Nodes) != 0 {
		t.Fatalf("legacy extension pull=%+v err=%v, want the expired notice this wave fixes", pulled, err)
	}
	p.report(controlSub, 0, 50)
	if got := p.cycleConsumed(controlSub); got != 0 {
		t.Fatalf("legacy extension charged %d to a cycle row that already ended", got)
	}
	var revokedBefore time.Time
	if err := admin.QueryRow(ctx, `SELECT expires_at FROM subscription_credentials
		WHERE subscription_id=$1::uuid AND status='revoked'`, controlSub).Scan(&revokedBefore); err != nil {
		t.Fatalf("read revoked credential: %v", err)
	}
	// 已经一致的行（主订阅等）不被 00102 改动
	untouched := p.snapshot(controlSub)
	p.runMigrationUp()
	if got := p.snapshot(controlSub); got != untouched || !strings.Contains(got, subID) {
		t.Fatalf("00102 changed rows that were already aligned:\n%s\n%s", untouched, got)
	}
	controlEnd := p.aligned("00102 resync", controlSub)
	var revokedAfter time.Time
	if err := admin.QueryRow(ctx, `SELECT expires_at FROM subscription_credentials
		WHERE subscription_id=$1::uuid AND status='revoked'`, controlSub).Scan(&revokedAfter); err != nil ||
		!revokedAfter.Equal(revokedBefore) {
		t.Fatalf("00102 touched a revoked credential: %s -> %s err=%v", revokedBefore, revokedAfter, err)
	}
	if _, err := p.pull(controlToken); err != nil {
		t.Fatalf("pull after 00102: %v", err)
	}
	p.report(controlSub, 0, 60)
	if got := p.cycleConsumed(controlSub); got != 60 {
		t.Fatalf("traffic after 00102 consumed=%d want 60", got)
	}
	// 重跑一次什么都不变
	afterOnce := p.snapshot(uuid.Nil.String())
	p.runMigrationUp()
	if again := p.snapshot(uuid.Nil.String()); again != afterOnce || !p.aligned("00102 rerun", controlSub).Equal(controlEnd) {
		t.Fatal("rerunning 00102 changed rows")
	}
	t.Log("marker=sub_period_pg18_migration_resync_ok")

	// 5) 后台加 7 天：三处一起改，审计、订阅事件、幂等记录同一事务落库。
	prevEnd := p.aligned("before admin extension", subID)
	extendClaim := orderReleasePG18Claim(t, ctx, conn.Conn(), p.fx.tenant, operator,
		SubscriptionExtendIdempotencyScope, "sp-extend")
	extended, err := p.billing.ExtendSubscriptionAsAdmin(ctx, p.fx.tenant, AdminExtendInput{
		SubscriptionID: subID, ActorID: operator, Days: 7, Reason: "  补偿线路故障  ", Claim: extendClaim,
	})
	if err != nil || extended.SubscriptionID != subID || extended.Days != 7 ||
		!extended.PreviousEnd.Equal(prevEnd) || !extended.PeriodEnd.Equal(prevEnd.UTC().AddDate(0, 0, 7)) ||
		!strings.Contains(extended.UserEmail, "buyer-") || extended.PreparedResponse().StatusCode() != 200 {
		t.Fatalf("admin extension=%+v err=%v", extended, err)
	}
	if end := p.aligned("admin extension", subID); !end.Equal(extended.PeriodEnd) {
		t.Fatalf("admin extension end=%s want %s", end, extended.PeriodEnd)
	}
	var audits, adminEvents int
	var claimStatus string
	var claimCode *int
	if err := admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='subscription.extended'
		          AND resource_type='subscription' AND resource_id=$2::uuid AND actor_id=$3::uuid
		          AND after_digest->>'reason'='补偿线路故障'),
		       (SELECT count(*) FROM subscription_events WHERE subscription_id=$2::uuid
		          AND event_type='extended' AND actor_kind='admin' AND actor_id=$3::uuid
		          AND payload->>'source'='admin' AND (payload->>'days')::int=7),
		       (SELECT status FROM idempotency_keys WHERE id=$4::uuid),
		       (SELECT response_code FROM idempotency_keys WHERE id=$4::uuid)`,
		p.fx.tenant, subID, operator, extendClaim.ID).Scan(&audits, &adminEvents, &claimStatus, &claimCode); err != nil ||
		audits != 1 || adminEvents != 1 || claimStatus != "succeeded" || claimCode == nil || *claimCode != 200 {
		t.Fatalf("admin extension evidence audits=%d events=%d claim=%s/%v err=%v",
			audits, adminEvents, claimStatus, claimCode, err)
	}
	if _, err := p.pull(token); err != nil {
		t.Fatalf("pull after admin extension: %v", err)
	}
	t.Log("marker=sub_period_pg18_admin_extension_ok")

	// 6) 续费与变更套餐之后三处同样一致。
	renewClaim := orderReleasePG18Claim(t, ctx, conn.Conn(), p.fx.tenant, buyer, RenewalIdempotencyScope, "sp-renew")
	renewal, err := p.billing.CreateRenewal(ctx, p.fx.tenant, CreateRenewalInput{
		UserID: buyer, SubscriptionID: subID, Claim: renewClaim,
	})
	if err != nil || renewal.Status != "pending_payment" {
		t.Fatalf("renewal=%+v err=%v", renewal, err)
	}
	beforeRenewal := p.aligned("before renewal", subID)
	p.pay("sp-renew", renewal.OrderID, renewal.PayableAmount)
	// 提前续费从当前周期末往后延一个月（月长 28–31 天）；凭据跟到新周期末，本期 cycle 行
	// 留在原到期日、已用量不动（w5expiry 规则 5），到点由 RollQuotaPeriods 滚进新周期
	var renewedEnd, cycleEnd time.Time
	var credsOff int
	if err := admin.QueryRow(ctx, `
		SELECT s.current_period_end,
		       (SELECT max(q.period_end) FROM quota_balances q
		         WHERE q.subscription_id = s.id AND q.period = 'cycle'),
		       (SELECT count(*) FROM subscription_credentials c
		         WHERE c.subscription_id = s.id AND c.status = 'active'
		           AND c.expires_at IS DISTINCT FROM s.current_period_end)
		  FROM subscriptions s WHERE s.id = $1::uuid`, subID).Scan(&renewedEnd, &cycleEnd, &credsOff); err != nil ||
		credsOff != 0 || !cycleEnd.Equal(beforeRenewal) ||
		renewedEnd.Before(beforeRenewal.AddDate(0, 0, 28)) || renewedEnd.After(beforeRenewal.AddDate(0, 0, 31)) {
		t.Fatalf("early renewal end=%s cycle=%s creds off=%d after %s err=%v",
			renewedEnd, cycleEnd, credsOff, beforeRenewal, err)
	}
	changeClaim := orderReleasePG18Claim(t, ctx, conn.Conn(), p.fx.tenant, buyer, PlanChangeIdempotencyScope, "sp-change")
	change, err := p.billing.CreatePlanChange(ctx, p.fx.tenant, PlanChangeInput{
		UserID: buyer, SubscriptionID: subID, PlanID: planB, PriceID: priceB, Claim: changeClaim,
	})
	if err != nil {
		t.Fatalf("plan change: %v", err)
	}
	if change.Status == "pending_payment" {
		p.pay("sp-change", change.OrderID, change.PayableAmount)
	}
	p.aligned("plan change", subID)
	if _, err := p.pull(token); err != nil {
		t.Fatalf("pull after plan change: %v", err)
	}
	t.Log("marker=sub_period_pg18_four_paths_aligned_ok")

	// 7) 拒绝：不存在的订阅、非生效订阅、没有到期时间的订阅；都不改任何东西。
	refuse := func(step, target string, code httpx.Code) {
		t.Helper()
		c := orderReleasePG18Claim(t, ctx, conn.Conn(), p.fx.tenant, operator,
			SubscriptionExtendIdempotencyScope, step)
		_, err := p.billing.ExtendSubscriptionAsAdmin(ctx, p.fx.tenant, AdminExtendInput{
			SubscriptionID: target, ActorID: operator, Days: 7, Reason: "补偿线路故障", Claim: c,
		})
		wantHTTPCode(t, step, err, code)
	}
	refuse("sp-missing", uuid.NewString(), httpx.CodeNotFound)
	// 单独一个用户：buyer 已有别的套餐的订阅，再兑套餐卡会在原订阅上换套餐（w6plan），
	// 这里要的是一条新开的订阅
	var pausedSub string
	pausedUser := uuid.NewString()
	p.must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($1,$2,$3,'Paused','active')`,
		pausedUser, p.fx.tenant, "sp-paused-"+pausedUser[:8]+"@example.test")
	orderReleasePG18InTxAs(t, ctx, app, p.fx.tenant, pausedUser, func(tx pgx.Tx) error {
		var err error
		pausedSub, _, _, _, err = p.billing.GiftGranter().GrantPlan(ctx, tx, p.fx.tenant, pausedUser, "", planA, priceA, purchase.Choice{})
		return err
	})
	p.must(`UPDATE subscriptions SET status='paused' WHERE id=$1::uuid`, pausedSub)
	frozen := p.snapshot(uuid.Nil.String())
	refuse("sp-paused", pausedSub, httpx.CodeConflict)
	if p.snapshot(uuid.Nil.String()) != frozen {
		t.Fatal("refusing a paused subscription changed rows")
	}
	p.must(`UPDATE subscriptions SET status='active', current_period_end=NULL WHERE id=$1::uuid`, pausedSub)
	frozen = p.snapshot(uuid.Nil.String())
	refuse("sp-no-end", pausedSub, httpx.CodeValidationFailed)
	if p.snapshot(uuid.Nil.String()) != frozen {
		t.Fatal("refusing a subscription without an end changed rows")
	}
	var refusedEvents int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM subscription_events
		WHERE subscription_id=$1::uuid AND event_type='extended'`, pausedSub).Scan(&refusedEvents); err != nil || refusedEvents != 0 {
		t.Fatalf("refused extensions wrote %d events err=%v", refusedEvents, err)
	}
	t.Log("marker=sub_period_pg18_refusals_ok")

	// 配额滚动（w3core）：年付套餐的月 / 日配额按自然周期起算与滚动，SKIP LOCKED 分批
	t.Run("quota periods roll on their own cycle", func(t *testing.T) {
		p.t = t
		checkQuotaRollPG18(t, p)
	})

	// 到期与续费规则（w5expiry，用户 2026-10-07）
	t.Run("expiry and renewal rules", func(t *testing.T) {
		p.t = t
		checkExpiryRulesPG18(t, p, conn.Conn())
	})

	// 换套餐的三个入口统一（w6plan，用户 2026-10-07）
	t.Run("plan change entries", func(t *testing.T) {
		p.t = t
		checkPlanChangeEntriesPG18(t, p, conn.Conn())
	})
}
