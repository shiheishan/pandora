package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestSubscriptionLabelPG18 钉住订阅备注名与配置名（购买模型统一 2.8，2026-10-07）：
//
//   - PATCH v1/me/subscriptions/{id} 起名、改名、清除；名字经 purchase.NormalizeLabel 规范化
//     （去首尾空白，过长、控制字符回 422 字段 label）；
//   - 同一个人的两份不能重名（不分大小写），撞上回 409 并点出占着名字的那一份；别人可以同名；
//   - 别人的订阅、不是 UUID 的 id 一律 404；
//   - 门户列表的 client_name 与订阅下载的 Content-Disposition 是同一个配置名：
//     「站点名 · 备注名」，没起名时「站点名 · 套餐名」；
//   - 改名不推进节点下发纪元（00135 拆开的订阅纪元触发器排除 label）。
func TestSubscriptionLabelPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant  = "7c1a0000-0000-4000-8000-000000000001"
		user    = "7c1a0000-0000-4000-8000-000000000021"
		other   = "7c1a0000-0000-4000-8000-000000000022"
		product = "7c1a0000-0000-4000-8000-000000000031"
		plan    = "7c1a0000-0000-4000-8000-000000000032"
		planVer = "7c1a0000-0000-4000-8000-000000000033"
		pool    = "7c1a0000-0000-4000-8000-000000000041"
		server  = "7c1a0000-0000-4000-8000-000000000051"
		node    = "7c1a0000-0000-4000-8000-000000000061"
		subA    = "7c1a0000-0000-4000-8000-000000000071"
		subB    = "7c1a0000-0000-4000-8000-000000000072"
		subC    = "7c1a0000-0000-4000-8000-000000000073"
		prefix  = "1abe1c0de123"
	)
	// 虚构令牌：低熵、互不相同、长度过 16 的门槛
	tokA, tokB, tokC := strings.Repeat("laba", 9), strings.Repeat("labb", 9), strings.Repeat("labc", 9)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed label fixture: %v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency,sub_path_prefix) VALUES($1,'label-pg18','Label PG18','CNY',$2)`, tenant, prefix)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
	      ($2,$1,'label@label.invalid','Label','active'),($3,$1,'other@label.invalid','Other','active')`, tenant, user, other)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'label-product','Label Product','active')`, tenant, product)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'label','Label','active')`, tenant, pool)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'label-plan','基础版','draft')`, tenant, product, plan)
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, tenant, plan, planVer, user)
	must(`INSERT INTO plan_node_pools(tenant_id,plan_version_id,pool_id) VALUES($1,$2,$3)`, tenant, planVer, pool)
	must(`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, planVer)
	must(`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, tenant, planVer, plan)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'label-server','ready')`, tenant, server)
	must(`INSERT INTO nodes(id,tenant_id,name,display_name,pool_id,status,node_type,server_host,server_port,
			server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at,sort_order)
		  VALUES($2,$1,'Label Line','Label Line',$3,'active','vless','label.pull.invalid',443,$4,'active',1,now(),now(),0)`,
		tenant, node, pool, server)
	for _, s := range []struct{ id, user, token, age string }{
		{subA, user, tokA, "2 days"}, {subB, user, tokB, "1 day"}, {subC, other, tokC, "1 day"},
	} {
		must(`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
				current_period_start,current_period_end,created_at)
			  VALUES($1,$2,$3,$4,$5,'active','CNY',100,now()-interval '10 days',now()+interval '20 days',now()-$6::interval)`,
			s.id, tenant, s.user, plan, planVer, s.age)
		must(`INSERT INTO subscription_credentials(tenant_id,subscription_id,user_id,token_hash,token_prefix,scope,expires_at)
			  VALUES($1,$2,$3,$4,$5,'subscription',now()+interval '20 days')`, tenant, s.id, s.user, crypto.HashToken(s.token), s.token[:8])
		must(`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
			  VALUES($1,$2,'traffic.bytes','cycle',now()-interval '10 days',now()+interval '20 days',1000,1000,10)`, tenant, s.id)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := subscription.New(app, []byte("label-pg18-salt"), nil)
	h := &handlers{d: Deps{Log: log, Subscription: svc, Cfg: &config.Config{PublicBaseURL: "https://portal.label.invalid"}}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c := httpx.WithTenantID(req.Context(), tenant)
			c = httpx.WithRequestID(c, "req-label-pg18")
			// 测试用请求头切换当前用户，缺省是 user
			who := req.Header.Get("X-Test-User")
			if who == "" {
				who = user
			}
			c = httpx.WithPrincipal(c, &httpx.Principal{Kind: "user", Audience: "public", UserID: who, TenantID: tenant})
			next.ServeHTTP(w, req.WithContext(c))
		})
	})
	r.Get("/{prefix}/{token}", h.subscribe)
	r.Get("/v1/me/subscriptions", h.listSubscriptions)
	r.Patch("/v1/me/subscriptions/{id}", h.renameSubscription)

	type errorBody struct {
		Error struct {
			Code    string            `json:"code"`
			Message string            `json:"message"`
			Fields  map[string]string `json:"fields"`
		} `json:"error"`
	}
	type renamed struct {
		Label      *string `json:"label"`
		ClientName string  `json:"client_name"`
	}
	rename := func(as, id, body string) (int, renamed, errorBody) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/v1/me/subscriptions/"+id, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		if as != "" {
			req.Header.Set("X-Test-User", as)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var ok renamed
		var fail errorBody
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &ok); err != nil {
				t.Fatalf("decode rename response %s: %v", w.Body, err)
			}
		} else {
			_ = json.Unmarshal(w.Body.Bytes(), &fail)
		}
		return w.Code, ok, fail
	}
	disposition := func(token string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/"+prefix+"/"+token+".yaml", nil).WithContext(ctx)
		req.Header.Set("User-Agent", "clash-verge/v2.0")
		req.Header.Set("X-Real-IP", "198.51.100.60")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("pull %s: status=%d body=%s", token[:4], w.Code, w.Body)
		}
		return w.Header().Get("Content-Disposition")
	}
	list := func() map[string]renamed {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/me/subscriptions", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out struct {
			Subscriptions []struct {
				ID string `json:"id"`
				renamed
			} `json:"subscriptions"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("list subscriptions: status=%d body=%s", w.Code, w.Body)
		}
		m := map[string]renamed{}
		for _, s := range out.Subscriptions {
			m[s.ID] = s.renamed
		}
		return m
	}
	epoch := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT last_value + is_called::int FROM node_delivery_epoch`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	planOnly := `attachment; filename="Pandora"; filename*=UTF-8''Pandora%20%C2%B7%20%E5%9F%BA%E7%A1%80%E7%89%88`

	// --- 没起名：配置名是「站点名 · 套餐名」，列表与下载同一个 ---
	if got := list(); len(got) != 2 || got[subA].Label != nil || got[subA].ClientName != "Pandora · 基础版" ||
		got[subB].ClientName != "Pandora · 基础版" {
		t.Fatalf("unnamed list=%+v", got)
	}
	if got := disposition(tokA); got != planOnly {
		t.Fatalf("unnamed Content-Disposition=%q want %q", got, planOnly)
	}

	// --- 起名：去首尾空白；下载的配置名跟着变；不推进下发纪元 ---
	before := epoch()
	code, out, _ := rename("", subA, `{"label":"  妈妈的 iPad "}`)
	if code != http.StatusOK || out.Label == nil || *out.Label != "妈妈的 iPad" || out.ClientName != "Pandora · 妈妈的 iPad" {
		t.Fatalf("rename A: status=%d out=%+v", code, out)
	}
	if got := list(); got[subA].Label == nil || *got[subA].Label != "妈妈的 iPad" || got[subA].ClientName != out.ClientName {
		t.Fatalf("list after rename=%+v", got)
	}
	wantA := `attachment; filename="Pandora iPad"; filename*=UTF-8''Pandora%20%C2%B7%20%E5%A6%88%E5%A6%88%E7%9A%84%20iPad`
	if got := disposition(tokA); got != wantA {
		t.Fatalf("named Content-Disposition=%q want %q", got, wantA)
	}
	if got := disposition(tokB); got != planOnly {
		t.Fatalf("the other subscription's Content-Disposition changed: %q", got)
	}
	t.Log("marker=public_api_pg18_label_profile_name_ok")

	// --- 同一个人不能重名（不分大小写），409 点出占着名字的那一份；校验失败 422 ---
	code, _, fail := rename("", subB, `{"label":"妈妈的 IPAD"}`)
	if code != http.StatusConflict || fail.Error.Code != "conflict" ||
		!strings.Contains(fail.Error.Message, "「妈妈的 iPad · 基础版」") || fail.Error.Fields["label"] == "" {
		t.Fatalf("duplicate label: status=%d body=%+v", code, fail)
	}
	for name, body := range map[string]string{
		"too long":      `{"label":"一二三四五六七八九十一二三四五六七"}`,
		"control chars": `{"label":"a\nb"}`,
	} {
		if code, _, fail := rename("", subB, body); code != http.StatusUnprocessableEntity || fail.Error.Fields["label"] == "" {
			t.Fatalf("%s: status=%d body=%+v", name, code, fail)
		}
	}
	// 别人的订阅、不是 UUID 的 id：404，名字不动
	if code, _, _ := rename("", subC, `{"label":"偷改"}`); code != http.StatusNotFound {
		t.Fatalf("renaming another user's subscription: status=%d", code)
	}
	if code, _, _ := rename("", "not-a-uuid", `{"label":"x"}`); code != http.StatusNotFound {
		t.Fatalf("renaming a malformed id: status=%d", code)
	}
	// 别人可以用同一个名字
	if code, out, _ := rename(other, subC, `{"label":"妈妈的 iPad"}`); code != http.StatusOK || out.ClientName != "Pandora · 妈妈的 iPad" {
		t.Fatalf("another user taking the same name: status=%d out=%+v", code, out)
	}
	t.Log("marker=public_api_pg18_label_unique_per_user_ok")

	// --- 清除名字（null）后名字让出来：B 可以用；配置名回到套餐名 ---
	if code, out, _ := rename("", subA, `{"label":null}`); code != http.StatusOK || out.Label != nil || out.ClientName != "Pandora · 基础版" {
		t.Fatalf("clear A: status=%d out=%+v", code, out)
	}
	if code, out, _ := rename("", subB, `{"label":"妈妈的 iPad"}`); code != http.StatusOK || *out.Label != "妈妈的 iPad" {
		t.Fatalf("B takes the freed name: status=%d out=%+v", code, out)
	}
	if code, out, _ := rename("", subB, `{"label":"   "}`); code != http.StatusOK || out.Label != nil {
		t.Fatalf("blank clears B: status=%d out=%+v", code, out)
	}
	var labels int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM subscriptions WHERE tenant_id=$1 AND user_id=$2 AND label IS NOT NULL`,
		tenant, user).Scan(&labels); err != nil || labels != 0 {
		t.Fatalf("labels left after clearing=%d err=%v", labels, err)
	}
	if after := epoch(); after != before {
		t.Fatalf("renaming advanced the node delivery epoch: %d -> %d", before, after)
	}
	t.Log("marker=public_api_pg18_label_keeps_delivery_epoch_ok")

	// --- 改名留痕（w8walk 第 5 节第 2 条）：每次成功改名一条 subscription.label_changed，记改前改后；
	// 撞名、不合规、别人的都不记。本人成功 4 次：A 起名、A 清除、B 起名、B 清除；别人 1 次 ---
	var mine, others int
	if err := admin.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE actor_id=$2::uuid), count(*) FILTER (WHERE actor_id=$3::uuid)
		  FROM audit_events
		 WHERE tenant_id=$1 AND action='subscription.label_changed' AND actor_kind='user'
		   AND resource_type='subscription' AND api_domain='public' AND outcome='success'`,
		tenant, user, other).Scan(&mine, &others); err != nil || mine != 4 || others != 1 {
		t.Fatalf("label audits mine=%d others=%d err=%v", mine, others, err)
	}
	var beforeLabel, afterLabel *string
	if err := admin.QueryRow(ctx, `
		SELECT before_digest->>'label', after_digest->>'label' FROM audit_events
		 WHERE tenant_id=$1 AND action='subscription.label_changed' AND resource_id=$2::uuid
		 ORDER BY chain_seq LIMIT 1`, tenant, subA).Scan(&beforeLabel, &afterLabel); err != nil ||
		beforeLabel != nil || afterLabel == nil || *afterLabel != "妈妈的 iPad" {
		t.Fatalf("first label audit before=%v after=%v err=%v", beforeLabel, afterLabel, err)
	}
	t.Log("marker=public_api_pg18_label_change_audited_ok")
}
