package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
)

// newSitePanel 起一个假的「新主域名」：只回 /v1/site-config，后台保存主域名前的连通核对靠它通过。
func newSitePanel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/site-config" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"registration_mode":"open","email_verification":false,"password_reset":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 后台改了主域名：新单的回调与回跳地址立即用新域名；旧域名上打开的门户传来的回跳地址照常放行；
// CoinPayments 的验签候选同时有新旧两个域名（改之前建的发票按旧地址签名）。
func TestNewIntentUsesChangedSiteURL(t *testing.T) {
	e, ctx := newEnv(t)
	oldSite := "https://panel.example.test"
	newSite := newSitePanel(t).URL // 开发环境（UseSiteURLConfig 的 dev）收 http 与回环

	// 整例回滚的测试事务里提交后钩子不跑（快照失效靠它），所以改之前不读 SiteURL，免得快照里留下旧值；
	// 快照的提交后失效由 settings 的单测钉住
	if err := e.settings.SetSite(ctx, settingssvc.SiteUpdate{URL: &newSite}); err != nil {
		t.Fatalf("SetSite: %v", err)
	}

	user := e.mkUser(t, ctx)
	code := "fake-site-" + uuid.New().String()[:8]
	_ = e.mkFakeProvider(t, ctx, code)

	var got []provider.CreateRequest
	orig := e.svc.adapter
	e.svc.adapter = func(p providerRecord) (provider.Provider, error) {
		base, err := orig(p)
		if err != nil {
			return nil, err
		}
		return &createStubAdapter{inner: base, createFn: func(ctx context.Context, req provider.CreateRequest) (provider.CreateResponse, error) {
			got = append(got, req)
			return base.CreatePayment(ctx, req)
		}}, nil
	}
	t.Cleanup(func() { e.svc.adapter = orig })

	for _, tc := range []struct {
		supplied, want string
	}{
		{oldSite + "/#/orders/1", oldSite + "/#/orders/1"},           // 旧域名上的门户：仍是本站
		{newSite + "/#/orders/2", newSite + "/#/orders/2"},           // 新域名上的门户
		{"https://evil.example.test/#/orders", newSite + "/#orders"}, // 别的站：回当前站点的订单页
		{newSite + ".evil.test/#/x", newSite + "/#orders"},           // 前缀相同的别的主机
		{newSite + "@evil.test/#/x", newSite + "/#orders"},           // 带账号段的别的主机
		{newSite + `\evil.test/#/x`, newSite + "/#orders"},           // 反斜杠，浏览器当成 /
	} {
		orderID, orderNo := e.mkOrder(t, ctx, user)
		in := e.intentInput(code, orderID, orderNo, user)
		in.ReturnURL = tc.supplied
		if _, err := e.svc.CreateIntent(ctx, in); err != nil {
			t.Fatalf("CreateIntent(%s): %v", tc.supplied, err)
		}
		last := got[len(got)-1]
		if last.NotifyURL != newSite+webhookPathPrefix+code {
			t.Fatalf("NotifyURL = %q want new site", last.NotifyURL)
		}
		if last.ReturnURL != tc.want {
			t.Fatalf("ReturnURL(%s) = %q want %q", tc.supplied, last.ReturnURL, tc.want)
		}
	}

	cands, err := e.svc.notifyURLCandidates(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{newSite + webhookPathPrefix + code, oldSite + webhookPathPrefix + code}
	if !slices.Equal(cands, want) {
		t.Fatalf("candidates = %v want %v", cands, want)
	}

	ps, err := e.svc.PaymentSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ps.SiteURL != newSite || ps.EffectiveBase != newSite {
		t.Fatalf("PaymentSettings = %+v（后台回显应是当前生效的站点地址）", ps)
	}
}
