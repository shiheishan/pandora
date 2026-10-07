package billing

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/payment/epay"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 后台建 / 改支付渠道与易支付多方式（w2pay）：
//   - 建：先落行、再按 id 作 AAD 加密，loadProvider 用同一个 AAD 解开一致；
//   - 改：商户号、密钥留空不改，密文一字节不动；只改密钥时商户号沿用；
//   - offline 与非后台适配器只读；
//   - 换方式：先支付宝、退出、再选微信，拿到的是微信收银台（旧意图作废），
//     同方式连点仍复用同一个意图；不在渠道方式里的方式被拒。
func TestPaymentProviderAdminPG18(t *testing.T) {
	ctx, adminPool, app := pg18test.Open(t, pg18test.Fixture{
		Domain:         "PAYMENT_PROVIDER",
		DatabasePrefix: "pandora_payment_provider_gate",
		MarkerTable:    "pandora_payment_provider_test_marker",
		CommentTag:     "pandora-payment-provider-pg18",
	})
	conn, err := adminPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire fixture connection: %v", err)
	}
	defer conn.Release()
	admin := conn.Conn()
	fx := orderReleasePG18Seed(t, ctx, admin)

	masterKey := []byte("payment-provider-pg18-test-only-master-key")
	env, err := crypto.NewEnvelope(masterKey)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	service := NewService(app, nil)
	payments := NewPaymentService(service, app, env, masterKey, "https://panel.example.test", true)
	actor := ProviderActor{Kind: "admin", ID: fx.referrer}

	count := func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("count: %v\nSQL: %s", err, sql)
		}
		return n
	}
	sealedOf := func(t *testing.T, code string) (string, []byte) {
		t.Helper()
		var id string
		var sealed []byte
		if err := admin.QueryRow(ctx, `SELECT id, credentials_encrypted FROM payment_providers
			WHERE tenant_id=$1 AND code=$2`, fx.tenant, code).Scan(&id, &sealed); err != nil {
			t.Fatalf("read sealed credentials: %v", err)
		}
		return id, sealed
	}
	wantCode := func(t *testing.T, err error, code httpx.Code) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != code {
			t.Fatalf("err=%v, want %s", err, code)
		}
	}
	settings := func(merchant, key string, methods ...string) ProviderSettings {
		return ProviderSettings{
			DisplayName: "易支付 PG18", BaseURL: "http://127.0.0.1:9", AllowPrivateHost: true,
			Methods: methods, DefaultMethod: methods[0], MerchantID: merchant, Key: key,
		}
	}

	code := "w2pay-" + fx.suffix[:8]
	const merchant, keyA, keyB = "pg18-merchant-test-only", "pg18-key-A-test-only", "pg18-key-B-test-only"

	t.Run("create seals credentials bound to the row and loadProvider opens them", func(t *testing.T) {
		out, err := payments.CreateProvider(ctx, fx.tenant, actor, CreateProviderInput{
			Code: code, Adapter: "epay", ProviderSettings: settings(merchant, keyA, "alipay", "wxpay"),
			Enabled: true, AcceptingNew: false,
		})
		if err != nil || out.ID == "" || !out.CredentialsChanged {
			t.Fatalf("create out=%+v err=%v", out, err)
		}
		if count(t, `SELECT count(*) FROM payment_providers WHERE id=$1 AND tenant_id=$2 AND adapter='epay'
			AND enabled AND NOT accepting_new AND key_version=1 AND credentials_encrypted IS NOT NULL
			AND position(convert_to($3,'UTF8') IN credentials_encrypted)=0
			AND config->'methods' = '["alipay","wxpay"]'::jsonb AND config->>'default_method'='alipay'`,
			out.ID, fx.tenant, keyA) != 1 {
			t.Fatal("created row shape is wrong or the key is stored in clear")
		}
		rec, err := payments.loadProvider(ctx, fx.tenant, code)
		if err != nil || rec.ID != out.ID || rec.Credentials.MerchantID != merchant || rec.Credentials.Key != keyA {
			t.Fatalf("loadProvider rec=%+v err=%v", rec, err)
		}
		// AAD 绑定行 id：密文搬到另一行就解不开
		_, sealed := sealedOf(t, code)
		if _, err := env.Open(sealed, []byte("payment_provider:"+fx.provider)); err == nil {
			t.Fatal("ciphertext opened under another provider's AAD")
		}
		if count(t, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2
			AND action='payment_provider.create' AND actor_kind='admin' AND actor_id=$3
			AND after_digest->>'credentials_changed'='true'
			AND position($4 IN after_digest::text)=0 AND position($5 IN after_digest::text)=0`,
			fx.tenant, out.ID, fx.referrer, keyA, merchant) != 1 {
			t.Fatal("create audit missing or carries credentials")
		}
		_, err = payments.CreateProvider(ctx, fx.tenant, actor, CreateProviderInput{
			Code: code, Adapter: "epay", ProviderSettings: settings(merchant, keyA, "alipay"),
		})
		wantCode(t, err, httpx.CodeConflict)
	})

	t.Run("edit with blank credentials keeps the ciphertext", func(t *testing.T) {
		id, before := sealedOf(t, code)
		in := settings("", "", "alipay", "wxpay")
		in.DisplayName = "易支付 PG18 改"
		out, err := payments.UpdateProvider(ctx, fx.tenant, actor, UpdateProviderInput{Code: code, ProviderSettings: in})
		if err != nil || out.ID != id || out.CredentialsChanged {
			t.Fatalf("blank-credential update out=%+v err=%v", out, err)
		}
		_, after := sealedOf(t, code)
		if string(after) != string(before) {
			t.Fatal("blank credentials re-sealed the ciphertext")
		}
		rec, err := payments.loadProvider(ctx, fx.tenant, code)
		if err != nil || rec.Credentials.MerchantID != merchant || rec.Credentials.Key != keyA ||
			rec.DisplayName != "易支付 PG18 改" || len(providerMethods(rec.Config)) != 3 {
			t.Fatalf("after blank update rec=%+v err=%v", rec, err)
		}
		if count(t, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2
			AND action='payment_provider.update' AND after_digest->>'credentials_changed'='false'`,
			fx.tenant, id) != 1 {
			t.Fatal("update audit should record credentials_changed=false")
		}

		// 只填新密钥：商户号沿用，密文换新
		out, err = payments.UpdateProvider(ctx, fx.tenant, actor, UpdateProviderInput{
			Code: code, ProviderSettings: settings("", keyB, "alipay", "wxpay"),
		})
		if err != nil || !out.CredentialsChanged {
			t.Fatalf("key-only update out=%+v err=%v", out, err)
		}
		rec, err = payments.loadProvider(ctx, fx.tenant, code)
		if err != nil || rec.Credentials.MerchantID != merchant || rec.Credentials.Key != keyB {
			t.Fatalf("after key update rec=%+v err=%v", rec, err)
		}
		if count(t, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2
			AND action='payment_provider.update' AND after_digest->>'credentials_changed'='true'
			AND position($3 IN after_digest::text)=0 AND position($3 IN before_digest::text)=0`,
			fx.tenant, id, keyB) != 1 {
			t.Fatal("credential update audit missing or carries the key")
		}
	})

	t.Run("offline and non-admin adapters are read-only", func(t *testing.T) {
		_, err := payments.UpdateProvider(ctx, fx.tenant, actor, UpdateProviderInput{
			Code: OfflineProviderCode, ProviderSettings: settings("m", "k", "alipay"),
		})
		wantCode(t, err, httpx.CodeConflict)
		_, err = payments.UpdateProvider(ctx, fx.tenant, actor, UpdateProviderInput{
			Code: fx.providerCode, ProviderSettings: settings("m", "k", "alipay"),
		})
		wantCode(t, err, httpx.CodeConflict)
		_, err = payments.UpdateProvider(ctx, fx.tenant, actor, UpdateProviderInput{
			Code: "missing-" + fx.suffix[:6], ProviderSettings: settings("m", "k", "alipay"),
		})
		wantCode(t, err, httpx.CodeNotFound)
		// 另一个租户看不到这条渠道（RLS + 租户条件）
		_, err = payments.UpdateProvider(ctx, fx.shadowTenant, actor, UpdateProviderInput{
			Code: code, ProviderSettings: settings("m", "k", "alipay"),
		})
		wantCode(t, err, httpx.CodeNotFound)
		prod := NewPaymentService(service, app, env, masterKey, "https://panel.example.test", false)
		_, err = prod.CreateProvider(ctx, fx.tenant, actor, CreateProviderInput{
			Code: "prod-" + fx.suffix[:8], Adapter: "epay", ProviderSettings: settings("m", "k", "alipay"),
		})
		wantCode(t, err, httpx.CodeValidationFailed)
	})

	t.Run("switching method after leaving the cashier gets the new method", func(t *testing.T) {
		if _, err := admin.Exec(ctx, `UPDATE payment_providers SET accepting_new=true
			WHERE tenant_id=$1 AND code=$2`, fx.tenant, code); err != nil {
			t.Fatalf("open provider for new payments: %v", err)
		}
		payments.Factory().Invalidate(fx.tenant, code)

		buyer := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO users(id,tenant_id,email,display_name,status)
			VALUES($1,$2,$3,'Method Switch Buyer','active')`,
			buyer, fx.tenant, "w2pay-"+buyer[:8]+"@example.test"); err != nil {
			t.Fatalf("insert buyer: %v", err)
		}
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, buyer, CheckoutIdempotencyScope, "w2pay-switch")
		order, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: buyer, PlanID: fx.plan, PriceID: fx.price, Claim: claim,
		})
		if err != nil || order.Status != "pending_payment" || order.PayableAmount != 1000 {
			t.Fatalf("create order=%+v err=%v", order, err)
		}
		pay := func(method string) (*CreateIntentOutput, error) {
			return payments.CreatePaymentIntent(ctx, fx.tenant, CreateIntentInput{
				OrderID: order.OrderID, UserID: buyer, ProviderCode: code, Method: method,
			})
		}
		mustPay := func(t *testing.T, method, wantType string, wantReused bool) *CreateIntentOutput {
			t.Helper()
			out, err := pay(method)
			if err != nil || out.Reused != wantReused || !strings.Contains(out.RedirectURL, "type="+wantType) {
				t.Fatalf("pay %q out=%+v err=%v, want type=%s reused=%v", method, out, err, wantType, wantReused)
			}
			return out
		}
		intentState := func(t *testing.T, id string) (status, method string) {
			t.Helper()
			if err := admin.QueryRow(ctx, `SELECT status, coalesce(action_payload->>'method','')
				FROM payment_intents WHERE id=$1`, id).Scan(&status, &method); err != nil {
				t.Fatalf("read intent: %v", err)
			}
			return status, method
		}

		ali := mustPay(t, "alipay", "alipay", false)
		if again := mustPay(t, "alipay", "alipay", true); again.IntentID != ali.IntentID {
			t.Fatalf("double click created a second intent %s != %s", again.IntentID, ali.IntentID)
		}
		// 用户退出支付宝收银台，回来改选微信
		wx := mustPay(t, "wxpay", "wxpay", false)
		if wx.IntentID == ali.IntentID {
			t.Fatal("switching to wxpay reused the alipay cashier")
		}
		if status, _ := intentState(t, ali.IntentID); status != "cancelled" {
			t.Fatalf("old alipay intent status=%s, want cancelled", status)
		}
		if status, method := intentState(t, wx.IntentID); status != "requires_action" || method != "wxpay" {
			t.Fatalf("wxpay intent status=%s method=%s", status, method)
		}
		if again := mustPay(t, "wxpay", "wxpay", true); again.IntentID != wx.IntentID {
			t.Fatal("repeat wxpay did not reuse the wxpay intent")
		}

		// 不在渠道方式里：拒绝，在途意图不动
		_, err = pay("qqpay")
		wantCode(t, err, httpx.CodeValidationFailed)
		if status, _ := intentState(t, wx.IntentID); status != "requires_action" {
			t.Fatalf("rejected method touched the active intent: %s", status)
		}

		// 没选方式 = 渠道默认（alipay），与在途的微信不同，重建
		def := mustPay(t, "", "alipay", false)
		if _, method := intentState(t, def.IntentID); method != "alipay" {
			t.Fatalf("default method recorded as %q", method)
		}

		// 本改动之前建的意图没有 method 键：按方式不同重建，不猜它当时用的哪种
		if _, err := admin.Exec(ctx, `UPDATE payment_intents SET action_payload = action_payload - 'method'
			WHERE id=$1`, def.IntentID); err != nil {
			t.Fatalf("strip method from payload: %v", err)
		}
		legacy := mustPay(t, "alipay", "alipay", false)
		if legacy.IntentID == def.IntentID {
			t.Fatal("legacy payload without method was reused")
		}
		if count(t, `SELECT count(*) FROM payment_intents WHERE order_id=$1::uuid
			AND status IN ('created','requires_action','processing')`, order.OrderID) != 1 {
			t.Fatal("an order must keep exactly one active intent")
		}
	})

	// 同一订单换方式（w5account，用户 2026-10-07 定）：换方式时对外单号用「订单号-序号」，
	// 旧意图作废；回调先按 provider_ref 找到订单。先付了新的那笔结清订单，旧单号那笔迟到的
	// 回调走「意外付款」挂账（excess_capture），最后只入账一次。
	t.Run("a switched order settles once and the late old callback is quarantined", func(t *testing.T) {
		buyer := uuid.NewString()
		if _, err := admin.Exec(ctx, `INSERT INTO users(id,tenant_id,email,display_name,status)
			VALUES($1,$2,$3,'Out Trade No Buyer','active')`,
			buyer, fx.tenant, "w5pay-"+buyer[:8]+"@example.test"); err != nil {
			t.Fatalf("insert buyer: %v", err)
		}
		claim := orderReleasePG18Claim(t, ctx, admin, fx.tenant, buyer, CheckoutIdempotencyScope, "w5pay-switch")
		order, err := service.CreateOrder(ctx, fx.tenant, CreateOrderInput{
			UserID: buyer, PlanID: fx.plan, PriceID: fx.price, Claim: claim,
		})
		if err != nil || order.PayableAmount != 1000 {
			t.Fatalf("create order=%+v err=%v", order, err)
		}
		pay := func(method string) *CreateIntentOutput {
			t.Helper()
			out, err := payments.CreatePaymentIntent(ctx, fx.tenant, CreateIntentInput{
				OrderID: order.OrderID, UserID: buyer, ProviderCode: code, Method: method,
			})
			if err != nil {
				t.Fatalf("pay %s: %v", method, err)
			}
			return out
		}
		refOf := func(intentID string) (status, ref, payloadRef string) {
			t.Helper()
			if err := admin.QueryRow(ctx, `SELECT status, coalesce(provider_ref,''),
				coalesce(action_payload->>'out_trade_no','') FROM payment_intents WHERE id=$1`, intentID).
				Scan(&status, &ref, &payloadRef); err != nil {
				t.Fatal(err)
			}
			return
		}
		ali := pay("alipay")
		if _, ref, payloadRef := refOf(ali.IntentID); ref != order.OrderNo || payloadRef != order.OrderNo ||
			!strings.Contains(ali.RedirectURL, "out_trade_no="+order.OrderNo+"&") {
			t.Fatalf("first intent ref=%q payload=%q url=%s, want the plain order number", ref, payloadRef, ali.RedirectURL)
		}
		wx := pay("wxpay")
		second := order.OrderNo + "-2"
		if status, ref, payloadRef := refOf(wx.IntentID); ref != second || payloadRef != second ||
			!strings.Contains(wx.RedirectURL, "out_trade_no="+second) || status != "requires_action" {
			t.Fatalf("switched intent status=%s ref=%q payload=%q url=%s, want %s", status, ref, payloadRef, wx.RedirectURL, second)
		}
		if status, _, _ := refOf(ali.IntentID); status != "cancelled" {
			t.Fatalf("old alipay intent status=%s, want cancelled", status)
		}

		rec, err := payments.loadProvider(ctx, fx.tenant, code)
		if err != nil {
			t.Fatal(err)
		}
		notify := func(outTradeNo, tradeNo, method string) *PaymentWebhookOutput {
			t.Helper()
			params := map[string]string{
				"pid": rec.Credentials.MerchantID, "trade_no": tradeNo, "out_trade_no": outTradeNo,
				"type": method, "name": "pg18", "money": "10.00", "trade_status": "TRADE_SUCCESS",
			}
			params["sign"] = epay.Sign(params, rec.Credentials.Key)
			params["sign_type"] = "MD5"
			q := url.Values{}
			for k, v := range params {
				q.Set(k, v)
			}
			req := httptest.NewRequest(http.MethodGet, "/v1/webhooks/payments/"+code+"?"+q.Encode(), nil)
			parsed, err := payments.ParseNotification(ctx, fx.tenant, code, req)
			if err != nil || !parsed.Input.SignatureVerified || parsed.Input.OrderID != order.OrderID {
				t.Fatalf("parse %s: input=%+v err=%v, want located by provider_ref", outTradeNo, parsed, err)
			}
			out, err := service.HandlePaymentWebhook(ctx, fx.tenant, parsed.Input)
			if err != nil {
				t.Fatalf("settle %s: %v", outTradeNo, err)
			}
			return out
		}
		paid := notify(second, "w5pay-wx-"+fx.suffix, "wxpay")
		if !paid.Processed || paid.QuarantineKind != "" || paid.LedgerTxnID == "" {
			t.Fatalf("wxpay settlement=%+v", paid)
		}
		late := notify(order.OrderNo, "w5pay-ali-"+fx.suffix, "alipay")
		if late.QuarantineKind != "excess_capture" {
			t.Fatalf("late alipay callback=%+v, want the unexpected-payment queue", late)
		}
		if n := count(t, `SELECT count(*) FROM ledger_transactions
			WHERE source_type='order' AND source_id=$1::uuid AND kind='order_paid'`, order.OrderID); n != 1 {
			t.Fatalf("order booked %d times, want once", n)
		}
		if n := count(t, `SELECT count(*) FROM late_payment_cases WHERE order_id=$1::uuid AND case_kind='excess_capture'`,
			order.OrderID); n != 1 {
			t.Fatalf("late payment cases=%d, want 1", n)
		}
		if n := count(t, `SELECT count(*) FROM payment_intents WHERE id=$1 AND status='succeeded'`, wx.IntentID); n != 1 {
			t.Fatal("the wxpay intent must be the one that succeeded")
		}
		// 第三次发起（订单已付）被拒，不会再生成新单号
		if _, err := payments.CreatePaymentIntent(ctx, fx.tenant, CreateIntentInput{
			OrderID: order.OrderID, UserID: buyer, ProviderCode: code, Method: "alipay",
		}); err == nil {
			t.Fatal("a paid order must not start another payment")
		}
	})
}
