// [INPUT]: 依赖 announcement 域的一次性库（openAnnouncementPG18）、step3/step4 的造数与请求辅助，依赖第 ⑤ 步的处理器
// [OUTPUT]: 对外提供 step5Router 与 TestSiteSettingsPG18
// [POS]: api/admin 第 ⑤ 步的 PG18 集成测试：站点时区的迁移默认值、读写、校验与审计；由 run-pg18-gates.sh 的 announcement 域按精确名单跑
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// step5Router 把被测处理器挂在一个带租户与主体的路由上；主体带会话且刚重认证过。
// 权限与重认证中间件不在这里验，由 TestStep5RouteProtections 钉住路由声明。
func step5Router(tenant, actor string, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, SessionID: "0190a000-0000-7000-8000-00000000c0de",
				ReauthedRecently: true})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	mount(r)
	return r
}

func TestSiteSettingsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000001"
		actor  = "87000000-0000-4000-8000-000000000011"
	)
	// 迁移 00083：新租户默认 Asia/Shanghai；种子里的默认租户原是 'UTC'，被改掉
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','site-tz-pg18','Site TZ','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@site-tz.invalid','Ops','active')`)
	var seeded, defaultTenant string
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenant).Scan(&seeded); err != nil || seeded != "Asia/Shanghai" {
		t.Fatalf("new tenant timezone=%q err=%v, want the Asia/Shanghai default", seeded, err)
	}
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE slug='default'`).Scan(&defaultTenant); err != nil || defaultTenant != "Asia/Shanghai" {
		t.Fatalf("seeded default tenant timezone=%q err=%v, want UTC migrated to Asia/Shanghai", defaultTenant, err)
	}

	h := step4Handlers(t, app)
	r := step5Router(tenant, actor, func(r chi.Router) {
		r.Get("/v1/settings/site", h.getSiteSettings)
		r.Post("/v1/settings/site", h.setSiteSettings)
	})
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/settings/site", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"timezone":"Asia/Shanghai"`) {
		t.Fatalf("get: status=%d body=%s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`{"timezone":""}`, `{"timezone":"Local"}`, `{"timezone":"Mars/Olympus"}`, `{}`} {
		w := step3Do(t, ctx, r, http.MethodPost, "/v1/settings/site", bad)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"timezone":"不是有效的时区"`) {
			t.Fatalf("bad %s: status=%d body=%s", bad, w.Code, w.Body.String())
		}
	}
	w := step3Do(t, ctx, r, http.MethodPost, "/v1/settings/site", `{"timezone":"America/New_York"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"timezone":"America/New_York"`) {
		t.Fatalf("set: status=%d body=%s", w.Code, w.Body.String())
	}
	var stored, before, after, auth string
	if err := admin.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id=$1`, tenant).Scan(&stored); err != nil || stored != "America/New_York" {
		t.Fatalf("stored timezone=%q err=%v", stored, err)
	}
	if err := admin.QueryRow(ctx, `SELECT before_digest->>'timezone', after_digest->>'timezone', auth_context
		FROM audit_events WHERE tenant_id=$1 AND action='site.timezone_changed' AND actor_id=$2`, tenant, actor).
		Scan(&before, &after, &auth); err != nil || before != "Asia/Shanghai" || after != "America/New_York" || auth != "reauth" {
		t.Fatalf("audit before=%q after=%q auth=%q err=%v", before, after, auth, err)
	}
	if w := step3Do(t, ctx, r, http.MethodGet, "/v1/settings/site", ""); !strings.Contains(w.Body.String(), `"timezone":"America/New_York"`) {
		t.Fatalf("get after set: %s", w.Body.String())
	}
}
