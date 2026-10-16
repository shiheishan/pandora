package service

import (
	"testing"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func epaySettings(merchantID, key, baseURL string) ProviderSettings {
	return ProviderSettings{
		DisplayName: "易支付测试",
		BaseURL:     baseURL,
		Methods:     []string{"alipay", "wxpay"},
		MerchantID:  merchantID,
		Key:         key,
	}
}

func findProvider(t *testing.T, views []ProviderView, code string) ProviderView {
	t.Helper()
	for _, v := range views {
		if v.Code == code {
			return v
		}
	}
	t.Fatalf("ListProviders 里没有 %s", code)
	return ProviderView{}
}

// 建、列、改易支付渠道：凭据加密落库、解密还原；只写不读；改站点地址必须重填密钥。
func TestProviderAdminCredentialsRoundTrip(t *testing.T) {
	e, ctx := newEnv(t)
	code := "epay-" + uuid.New().String()[:8]
	const key1 = "fake-key-aaaa"
	const key2 = "fake-key-bbbb"

	res, err := e.svc.CreateProvider(ctx, CreateProviderInput{
		Code: code, Adapter: AdapterEpay,
		ProviderSettings: epaySettings("1001", key1, "https://pay.example.test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.CredentialsChanged || res.Code != code || res.ID == uuid.Nil {
		t.Fatalf("create result %+v", res)
	}
	if e.count(t, ctx, `
SELECT count(*) FROM payment_providers
WHERE code = ? AND position(convert_to(?, 'UTF8') IN credentials_sealed) = 0`, code, key1) != 1 {
		t.Fatal("密钥不应以明文落库")
	}
	rec, err := e.svc.loadProvider(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Creds.MerchantID != "1001" || rec.Creds.Key != key1 {
		t.Fatalf("解密后的凭据不对: merchant=%q", rec.Creds.MerchantID)
	}

	views, err := e.svc.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := findProvider(t, views, code)
	if !v.HasCredentials || v.Adapter != AdapterEpay || !v.Enabled || v.AcceptingNew {
		t.Fatalf("view %+v", v)
	}
	if v.BaseURL != "https://pay.example.test" || len(v.Methods) != 2 || v.DefaultMethod != "alipay" {
		t.Fatalf("view settings %+v", v)
	}
	if !v.Today.Equal(decimal.Zero) || v.SuccessRate24h != nil || v.LastCallbackAt != nil {
		t.Fatalf("新渠道不应有统计: %+v", v)
	}

	// 只改商户号：密钥保留
	up, err := e.svc.UpdateProvider(ctx, code, epaySettings("1002", "", "https://pay.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if !up.CredentialsChanged {
		t.Fatal("改商户号应算凭据变更")
	}
	rec, err = e.svc.loadProvider(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Creds.MerchantID != "1002" || rec.Creds.Key != key1 {
		t.Fatalf("只改商户号后: merchant=%q 密钥是否保留=%v", rec.Creds.MerchantID, rec.Creds.Key == key1)
	}

	// 什么凭据都不填：不算变更
	up, err = e.svc.UpdateProvider(ctx, code, epaySettings("", "", "https://pay.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if up.CredentialsChanged {
		t.Fatal("没填凭据不应算变更")
	}

	// 改站点地址不填密钥：拒绝
	_, err = e.svc.UpdateProvider(ctx, code, epaySettings("", "", "https://pay2.example.test"))
	if ae := errs.As(err); err == nil || ae.Code != errs.ValidationFailed || ae.Fields["key"] == "" {
		t.Fatalf("改地址不填密钥应 ValidationFailed(key)，得 %v", err)
	}
	rec, err = e.svc.loadProvider(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if rec.BaseURL != "https://pay.example.test" {
		t.Fatalf("被拒的编辑不应落库: %s", rec.BaseURL)
	}

	// 改站点地址并重填密钥：通过
	up, err = e.svc.UpdateProvider(ctx, code, epaySettings("", key2, "https://pay2.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if !up.CredentialsChanged {
		t.Fatal("换密钥应算凭据变更")
	}
	rec, err = e.svc.loadProvider(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Creds.Key != key2 || rec.Creds.MerchantID != "1002" || rec.BaseURL != "https://pay2.example.test" {
		t.Fatalf("改地址换密钥后: merchant=%q base=%s 密钥已换=%v", rec.Creds.MerchantID, rec.BaseURL, rec.Creds.Key == key2)
	}

	// 重复编码
	_, err = e.svc.CreateProvider(ctx, CreateProviderInput{
		Code: code, Adapter: AdapterEpay,
		ProviderSettings: epaySettings("1001", key1, "https://pay.example.test"),
	})
	if errs.As(err).Code != errs.Conflict {
		t.Fatalf("重复编码应 Conflict，得 %v", err)
	}
}

// 今日金额按站点时区算日界；成功率看近 24 小时终态意图；最近回调只看验过签的事件。
func TestListProvidersStatsInSiteTimezone(t *testing.T) {
	e, ctx := newEnv(t)
	// UTC+14：本地零点在 UTC 当天 10:00，零点前后各一分钟在 UTC 里总是同一天，
	// 按 UTC 算要么都算进今天、要么都不算；按站点时区只算零点后那笔。
	const tz = "Pacific/Kiritimati"
	tzName := tz
	if err := e.settings.SetSite(ctx, settingssvc.SiteUpdate{Timezone: &tzName}); err != nil {
		t.Fatal(err)
	}
	user := e.mkUser(t, ctx)
	code := "fake-stats-" + uuid.New().String()[:8]
	p := e.mkFakeProvider(t, ctx, code)

	var localStart time.Time
	if err := e.row(ctx, `SELECT date_trunc('day', now() AT TIME ZONE ?) AT TIME ZONE ?`, tz, tz).Scan(&localStart); err != nil {
		t.Fatal(err)
	}
	insertPay := func(ref string, amount string, at time.Time) {
		t.Helper()
		orderID, _ := e.mkOrder(t, ctx, user)
		if err := e.exec(ctx, `
INSERT INTO payments (order_id, user_id, provider_id, provider_payment_id, amount, paid_at)
VALUES (?, ?, ?, ?, ?, ?)`, orderID, user, p.ID, ref, amount, at); err != nil {
			t.Fatal(err)
		}
	}
	insertPay("stat-today", "7.00", localStart.Add(time.Minute))
	insertPay("stat-yesterday", "3.00", localStart.Add(-time.Minute))

	insertIntent := func(n int, status string) {
		t.Helper()
		orderID, orderNo := e.mkOrder(t, ctx, user)
		if err := e.exec(ctx, `
INSERT INTO payment_intents (order_id, order_no, user_id, provider_id, out_trade_no, amount, status, http_method, redirect_url)
VALUES (?, ?, ?, ?, ?, 1.00, ?, 'GET', 'https://checkout.example.test/x')`,
			orderID, orderNo, user, p.ID, "S-"+uuid.New().String()[:12], status); err != nil {
			t.Fatalf("intent %d: %v", n, err)
		}
	}
	insertIntent(1, "succeeded")
	insertIntent(2, "cancelled")
	insertIntent(3, "requires_action") // 不是终态，不计入

	var verifiedAt time.Time
	if err := e.row(ctx, `
INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified, received_at)
VALUES (?, 'ev-verified', 'payment.succeeded', true, now() - interval '1 hour')
RETURNING received_at`, p.ID).Scan(&verifiedAt); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `
INSERT INTO payment_events (provider_id, provider_event_id, event_type, signature_verified, processing_status, processed_at, received_at)
VALUES (?, 'unverified:x', 'payment.unknown', false, 'ignored', now(), now())`, p.ID); err != nil {
		t.Fatal(err)
	}

	views, err := e.svc.ListProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	v := findProvider(t, views, code)
	if !v.Today.Equal(decimal.RequireFromString("7.00")) {
		t.Fatalf("今日金额应只算站点时区今天的 7.00: %v", v.Today)
	}
	if v.SuccessRate24h == nil || *v.SuccessRate24h != 0.5 {
		t.Fatalf("成功率应为 0.5: %v", v.SuccessRate24h)
	}
	if v.LastCallbackAt == nil || !v.LastCallbackAt.Equal(verifiedAt) {
		t.Fatalf("最近回调应是验过签的那条 %v，得 %v", verifiedAt, v.LastCallbackAt)
	}
}

// OrderHistory 按时间倒序给出意图与收款；OrderSummaries 一批订单里每个都有值。
func TestOrderHistoryAndSummaries(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx)
	code := "fake-hist-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)

	// order1：先发起 9.90，改金额轮换成 8.00，付了第二个
	order1, orderNo1 := e.mkOrder(t, ctx, user)
	first := e.createIntent(t, ctx, code, order1, orderNo1, user, "9.90")
	second := e.createIntent(t, ctx, code, order1, orderNo1, user, "8.00")
	if first.OutTradeNo == second.OutTradeNo {
		t.Fatal("改金额应轮换商户单号")
	}
	if _, err := e.svc.HandleWebhook(ctx, code, fakeInbound("evt-hist-1", "payment.succeeded", "pay-hist-1", second.OutTradeNo, 800)); err != nil {
		t.Fatal(err)
	}
	// order2：只发起过支付
	order2, orderNo2 := e.mkOrder(t, ctx, user)
	_ = e.createIntent(t, ctx, code, order2, orderNo2, user, "5.00")
	// order3：什么都没有
	order3, _ := e.mkOrder(t, ctx, user)
	// order4：线下收款，没有意图
	order4, orderNo4 := e.mkOrder(t, ctx, user)
	if _, err := e.svc.RecordOffline(ctx, OfflineInput{
		OrderID: order4, OrderNo: orderNo4, UserID: user,
		Amount: dec("2.00"), Reference: "BANK-HIST", Reason: "线下",
	}); err != nil {
		t.Fatal(err)
	}

	h, err := e.svc.OrderHistory(ctx, order1)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Intents) != 2 || len(h.Payments) != 1 {
		t.Fatalf("intents=%d payments=%d", len(h.Intents), len(h.Payments))
	}
	if h.Intents[0].ID != second.IntentID || h.Intents[0].Status != IntentSucceeded ||
		h.Intents[1].ID != first.IntentID || h.Intents[1].Status != IntentCancelled {
		t.Fatalf("意图应新的在前: %+v", h.Intents)
	}
	if h.Intents[0].ProviderCode != code || h.Intents[0].ProviderName != "Fake" || !h.Intents[0].Amount.Equal(dec("8.00")) {
		t.Fatalf("intent view %+v", h.Intents[0])
	}
	pv := h.Payments[0]
	if pv.IntentID == nil || *pv.IntentID != second.IntentID || !pv.Amount.Equal(dec("8.00")) ||
		pv.ProviderCode != code || pv.ProviderPaymentID != "pay-hist-1" {
		t.Fatalf("payment view %+v", pv)
	}

	empty, err := e.svc.OrderHistory(ctx, order3)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Intents == nil || empty.Payments == nil || len(empty.Intents) != 0 || len(empty.Payments) != 0 {
		t.Fatalf("没有记录应是空切片: %+v", empty)
	}

	sums, err := e.svc.OrderSummaries(ctx, []uuid.UUID{order1, order2, order3, order4})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 4 {
		t.Fatalf("每个请求的订单都应有值: %d", len(sums))
	}
	for _, id := range []uuid.UUID{order1, order2} {
		s := sums[id]
		if !s.HasIntent || s.ProviderCode == nil || *s.ProviderCode != code || s.ProviderName == nil || *s.ProviderName != "Fake" {
			t.Fatalf("order %s summary %+v", id, s)
		}
	}
	if s := sums[order3]; s.HasIntent || s.ProviderCode != nil || s.ProviderName != nil {
		t.Fatalf("没有记录应是零值: %+v", s)
	}
	if s := sums[order4]; s.HasIntent || s.ProviderCode == nil || *s.ProviderCode != OfflineProviderCode {
		t.Fatalf("线下收款应取收款的渠道: %+v", s)
	}
	// 支付方式只取收款：线下收款记 offline；只发起过支付、什么都没有的为 nil
	if s := sums[order4]; s.Method == nil || *s.Method != OfflineMethod {
		t.Fatalf("线下收款的方式应是 offline: %+v", s)
	}
	for _, id := range []uuid.UUID{order2, order3} {
		if s := sums[id]; s.Method != nil {
			t.Fatalf("没有收款不应有支付方式: %s %+v", id, s)
		}
	}

	none, err := e.svc.OrderSummaries(ctx, nil)
	if err != nil || len(none) != 0 {
		t.Fatalf("空输入: %v %v", none, err)
	}
}
