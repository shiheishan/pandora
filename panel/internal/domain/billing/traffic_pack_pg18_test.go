package billing

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// TestTrafficPackOrderPG18 证明流量包的购买与余额（D-E-1，迁移 00070）：
// 无订阅也能买；付款后发一笔余额；全额余额支付当场履约；多次购买叠加；
// 取消的单不发余额；下架的包买不到；礼品卡一码一笔；数据库守卫拒绝伪造与改写。
// 由 run-pg18-gates.sh 的 traffic_pack 域驱动，复用 order_release 的一次性租户夹具。
func TestTrafficPackOrderPG18(t *testing.T) {
	appDSN := os.Getenv("AEGIS_TRAFFIC_PACK_PG18_DSN")
	adminDSN := os.Getenv("AEGIS_TRAFFIC_PACK_PG18_ADMIN_DSN")
	if appDSN == "" || adminDSN == "" {
		t.Skip("AEGIS_TRAFFIC_PACK_PG18_DSN and AEGIS_TRAFFIC_PACK_PG18_ADMIN_DSN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open aegis_app pool: %v", err)
	}
	defer pool.Close()
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture connection: %v", err)
	}
	defer admin.Close(ctx)
	var database string
	if err := admin.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil ||
		database != os.Getenv("AEGIS_TRAFFIC_PACK_PG18_DATABASE") {
		t.Fatalf("refusing unexpected fixture database=%q err=%v", database, err)
	}
	orderReleasePG18AssertRuntimeTarget(t, ctx, pool, admin)

	fx := orderReleasePG18Seed(t, ctx, admin)
	service := NewService(pool, nil)
	packA, packB := uuid.NewString(), uuid.NewString()
	if _, err := admin.Exec(ctx, `INSERT INTO traffic_packs
		(id, tenant_id, name, traffic_bytes, currency, unit_amount, recommended, sort_order) VALUES
		($1, $3, 'Pack 1000', 1000, 'CNY', 1000, true, 1),
		($2, $3, 'Pack 500',   500, 'CNY',  600, false, 2)`, packA, packB, fx.tenant); err != nil {
		t.Fatalf("seed traffic packs: %v", err)
	}

	order := func(t *testing.T, label, packID string, useBalance int64) *CreateOrderOutput {
		t.Helper()
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer,
			CheckoutIdempotencyScope, label)
		out, err := service.CreateTrafficPackOrder(ctx, fx.tenant, CreateTrafficPackOrderInput{
			UserID: fx.buyer, PackID: packID, UseBalance: useBalance, Claim: claim,
		})
		if err != nil {
			t.Fatalf("CreateTrafficPackOrder(%s): %v", label, err)
		}
		return out
	}
	grants := func(t *testing.T) (int64, []TrafficPackGrant) {
		t.Helper()
		bal, err := service.MyTrafficPacks(ctx, fx.tenant, fx.buyer)
		if err != nil {
			t.Fatalf("MyTrafficPacks: %v", err)
		}
		return bal.RemainingBytesTotal, bal.Packs
	}
	orderStatus := func(t *testing.T, orderID string) string {
		t.Helper()
		var status string
		if err := admin.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1::uuid`, orderID).Scan(&status); err != nil {
			t.Fatalf("read order status: %v", err)
		}
		return status
	}

	packs, err := service.ListTrafficPacks(ctx, fx.tenant)
	if err != nil || len(packs) != 2 || packs[0].ID != packA || !packs[0].Recommended || packs[1].ID != packB {
		t.Fatalf("traffic pack catalog=%+v err=%v", packs, err)
	}

	// 1) 没有订阅也能买；付款回调后履约成一笔余额。
	paid := order(t, "pack-a-paid", packA, 0)
	if paid.Status != "pending_payment" || paid.PayableAmount != 1000 {
		t.Fatalf("pack order output=%+v", paid)
	}
	out, err := service.HandlePaymentWebhook(ctx, fx.tenant, PaymentWebhookInput{
		ProviderCode: fx.providerCode, ProviderEventID: "pack-a-event-" + fx.suffix,
		ProviderPaymentID: "pack-a-payment-" + fx.suffix, EventType: "payment.succeeded",
		OrderID: paid.OrderID, Amount: 1000, Currency: "CNY",
		RawPayload: map[string]any{"fixture": "traffic-pack"}, SignatureVerified: true,
	})
	if err != nil || out == nil || !out.Processed || out.SubscriptionID != "" {
		t.Fatalf("pack settlement output=%#v err=%v", out, err)
	}
	if got := orderStatus(t, paid.OrderID); got != "fulfilled" {
		t.Fatalf("paid pack order status=%s want fulfilled", got)
	}
	if remaining, list := grants(t); remaining != 1000 || len(list) != 1 ||
		list[0].Source != "order" || list[0].OrderID == nil || *list[0].OrderID != paid.OrderID {
		t.Fatalf("after paid pack remaining=%d grants=%+v", remaining, list)
	}
	t.Log("marker=traffic_pack_pg18_paid_order_granted_ok")

	// 2) 全额余额支付当场履约；再买一次就叠加。
	orderReleasePG18FundBalance(t, ctx, pool, fx.tenant, fx.buyer, 5000)
	byBalance := order(t, "pack-b-balance", packB, 600)
	if byBalance.Status != "fulfilled" || byBalance.PayableAmount != 0 || byBalance.BalanceApplied != 600 {
		t.Fatalf("balance-paid pack order output=%+v", byBalance)
	}
	if remaining, list := grants(t); remaining != 1500 || len(list) != 2 {
		t.Fatalf("stacked packs remaining=%d grants=%+v", remaining, list)
	}
	t.Log("marker=traffic_pack_pg18_zero_pay_and_stacking_ok")

	// 3) 取消的单（部分余额抵扣）不发余额。
	cancelled := order(t, "pack-a-cancel", packA, 300)
	if cancelled.Status != "pending_payment" || cancelled.PayableAmount != 700 {
		t.Fatalf("partial-balance pack order output=%+v", cancelled)
	}
	if _, err := service.CancelOrder(ctx, fx.tenant, fx.buyer, cancelled.OrderID); err != nil {
		t.Fatalf("cancel pack order: %v", err)
	}
	if got := orderStatus(t, cancelled.OrderID); got != "cancelled" {
		t.Fatalf("cancelled pack order status=%s", got)
	}
	if remaining, list := grants(t); remaining != 1500 || len(list) != 2 {
		t.Fatalf("cancelled order changed the balance remaining=%d grants=%+v", remaining, list)
	}

	// 4) 下架的包买不到。
	if _, err := admin.Exec(ctx, `UPDATE traffic_packs SET status='archived' WHERE id=$1`, packB); err != nil {
		t.Fatalf("archive pack: %v", err)
	}
	claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, fx.buyer, CheckoutIdempotencyScope, "pack-b-archived")
	_, err = service.CreateTrafficPackOrder(ctx, fx.tenant, CreateTrafficPackOrderInput{
		UserID: fx.buyer, PackID: packB, Claim: claim,
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeNotFound {
		t.Fatalf("archived pack order err=%v want not_found", err)
	}
	t.Log("marker=traffic_pack_pg18_cancel_and_archive_ok")

	// 5) 礼品卡流量进同一余额，一码一笔。
	codeID := uuid.NewString()
	grantGift := func() error {
		return pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.buyer}, func(tx pgx.Tx) error {
			return service.GiftGranter().GrantTraffic(ctx, tx, fx.tenant, fx.buyer, codeID, 250)
		})
	}
	if err := grantGift(); err != nil {
		t.Fatalf("gift traffic grant: %v", err)
	}
	if err := grantGift(); orderReleasePG18SQLState(err) != "23505" {
		t.Fatalf("second grant for the same code err=%v want unique violation", err)
	}
	if remaining, _ := grants(t); remaining != 1750 {
		t.Fatalf("gift traffic did not join the balance, remaining=%d", remaining)
	}
	t.Log("marker=traffic_pack_pg18_gift_card_grant_ok")

	// 6) 数据库守卫：伪造订单来源的余额、改容量、删除都被拒绝。
	forged := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant, ActorID: fx.buyer}, func(tx pgx.Tx) error {
		if _, err := GrantTrafficPackTx(ctx, tx, fx.tenant, fx.buyer, "order", uuid.NewString(), 999); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		return err
	})
	if state := orderReleasePG18SQLState(forged); state != "23514" {
		t.Fatalf("forged order grant SQLSTATE=%q err=%v", state, forged)
	}
	var grantID string
	if err := admin.QueryRow(ctx, `SELECT id::text FROM traffic_pack_grants
		WHERE tenant_id=$1 AND source='order' AND source_id=$2::uuid`, fx.tenant, paid.OrderID).Scan(&grantID); err != nil {
		t.Fatalf("read paid grant: %v", err)
	}
	for name, stmt := range map[string]string{
		"enlarge": `UPDATE traffic_pack_grants SET granted_bytes = granted_bytes + 1 WHERE id=$1::uuid`,
		"delete":  `DELETE FROM traffic_pack_grants WHERE id=$1::uuid`,
	} {
		err := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt, grantID)
			return err
		})
		if !orderReleasePG18RejectedByPrivilegeOrGuard(err) {
			t.Fatalf("%s grant SQLSTATE=%q err=%v", name, orderReleasePG18SQLState(err), err)
		}
	}
	var addonRetired bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conname='quota_balances_addon_retired_00070')`).Scan(&addonRetired); err != nil || !addonRetired {
		t.Fatalf("granted_addon must be pinned to zero, constraint present=%v err=%v", addonRetired, err)
	}
	t.Log("marker=traffic_pack_pg18_guards_ok")
}
