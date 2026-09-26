// [INPUT]: 依赖 announcement_pg18_test.go 的 openAnnouncementPG18（一次性 PG18 库护栏），依赖 devices.go 的 setDeviceLimit / setDeviceMode
// [OUTPUT]: 对外提供 TestDeviceLimitWritesPG18
// [POS]: api/admin 的 PG18 测试：设备上限两条写接口写审计，订阅不存在或 id 非法回 404 且不留痕（第 7.3 节）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestDeviceLimitWritesPG18(t *testing.T) {
	ctx, adminDB, app := openAnnouncementPG18(t)

	const (
		tenant  = "76000000-0000-4000-8000-000000000001"
		actor   = "76000000-0000-4000-8000-000000000011"
		owner   = "76000000-0000-4000-8000-000000000012"
		product = "76000000-0000-4000-8000-000000000021"
		plan    = "76000000-0000-4000-8000-000000000031"
		planVer = "76000000-0000-4000-8000-000000000041"
		sub     = "76000000-0000-4000-8000-000000000051"
	)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'device-limit-pg18','Device Limit PG18','USD')`, []any{tenant}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'operator@device-limit.invalid','Operator','active')`, []any{tenant, actor}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'owner@device-limit.invalid','Owner','active')`, []any{tenant, owner}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'device-product','Device Product','active')`, []any{tenant, product}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'device-plan','Device Plan','draft')`, []any{tenant, product, plan}},
		{`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, []any{tenant, plan, planVer, actor}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, []any{tenant, planVer}},
		{`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, []any{tenant, planVer, plan}},
		{`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($1,$2,$3,$4,$5,'active','USD',100)`, []any{sub, tenant, owner, plan, planVer}},
	} {
		if _, err := adminDB.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed device limit fixture: %v\nSQL: %s", err, row.sql)
		}
	}

	h := &handlers{d: Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "admin", Audience: "admin",
				UserID: actor, TenantID: tenant, Permissions: []string{"iam.user.write"}, ReauthedRecently: true})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Post("/v1/subscriptions/{id}/device-limit", h.setDeviceLimit)
	r.Post("/v1/settings/device-limit", h.setDeviceMode)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	auditCount := func(action string) int {
		var n int
		if err := adminDB.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`,
			tenant, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 反向：订阅不存在（原先回 200 ok）与非法 id（原先 500）都是 404，且不写审计
	for _, id := range []string{"76000000-0000-4000-8000-000000000099", "not-a-uuid"} {
		if w := post("/v1/subscriptions/"+id+"/device-limit", `{"limit":3}`); w.Code != http.StatusNotFound {
			t.Fatalf("device limit on %s: status=%d body=%s", id, w.Code, w.Body.String())
		}
	}
	if n := auditCount("subscription.device_limit_changed"); n != 0 {
		t.Fatalf("refused device limit writes left %d audits", n)
	}

	// 正向：改成 3，审计记下改前（NULL = 套餐规定）与改后
	if w := post("/v1/subscriptions/"+sub+"/device-limit", `{"limit":3}`); w.Code != http.StatusOK {
		t.Fatalf("device limit: status=%d body=%s", w.Code, w.Body.String())
	}
	var limit *int
	var before *string
	var after, auditActor string
	if err := adminDB.QueryRow(ctx, `SELECT device_limit FROM subscriptions WHERE id=$1`, sub).Scan(&limit); err != nil {
		t.Fatal(err)
	}
	if err := adminDB.QueryRow(ctx, `
		SELECT before_digest->>'device_limit', after_digest->>'device_limit', actor_id::text
		  FROM audit_events WHERE tenant_id=$1 AND action='subscription.device_limit_changed' AND resource_id=$2`,
		tenant, sub).Scan(&before, &after, &auditActor); err != nil {
		t.Fatalf("device limit change left no audit: %v", err)
	}
	if limit == nil || *limit != 3 || before != nil || after != "3" || auditActor != actor {
		t.Fatalf("device limit=%v audit before=%v after=%q actor=%q", limit, before, after, auditActor)
	}

	if w := post("/v1/settings/device-limit", `{"mode":"strict","grace":2}`); w.Code != http.StatusOK {
		t.Fatalf("device mode: status=%d body=%s", w.Code, w.Body.String())
	}
	var mode string
	if err := adminDB.QueryRow(ctx, `
		SELECT after_digest->>'mode' FROM audit_events
		 WHERE tenant_id=$1 AND action='device_limit.mode_changed' AND actor_id=$2`, tenant, actor).Scan(&mode); err != nil {
		t.Fatalf("device mode change left no audit: %v", err)
	}
	if mode != "strict" {
		t.Fatalf("device mode audit mode=%q", mode)
	}
}
