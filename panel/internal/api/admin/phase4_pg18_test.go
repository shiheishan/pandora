// [INPUT]: 依赖 announcement_pg18_test.go 的 openAnnouncementPG18、step3_pg18_test.go 的 step3Seed / step3Do / step3Nodes、step4_pg18_test.go 的 step4Handlers / step4Router，依赖 node_admin.go 与 appearance.go 的处理器
// [OUTPUT]: 对外提供 TestNodePatchKeepsSecretsPG18、TestPluginHookBoundsPG18
// [POS]: api/admin 的第 4 阶段后端三 PG18 测试：节点 PATCH 缺席的敏感键保留原值（R78）、钩子超时与重试次数越界回 422 且不落库（R93）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/plugin"
)

// R78：读接口抹掉敏感键，前端拿抹过的配置只改一个普通字段再 PATCH 回来，
// 库里的密钥必须还在；显式给新值才覆盖；换协议类型不把旧密钥带过去。
func TestNodePatchKeepsSecretsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000001"
		actor  = "87000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','node-secret-pg18','Node Secret','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@node-secret.invalid','Ops','active')`)
	nodes := step3Nodes(t, ctx, admin, tenant, "87000000-0000-4000-8000-", 2)
	hy2, stls := nodes[0], nodes[1]
	step3Seed(t, ctx, admin,
		`UPDATE nodes SET node_type='hysteria2', protocol_config='{"network":"udp","cert_path":"/etc/pandora/cert.pem","key_path":"/etc/pandora/key.pem","obfs":{"type":"salamander","password":"fixture-obfs"}}' WHERE id='`+hy2+`'`,
		`UPDATE nodes SET node_type='shadowtls', protocol_config='{"network":"tcp","version":3,"password":"fixture-outer","server":"a.example.com:443","method":"aes-256-gcm","strict":true}' WHERE id='`+stls+`'`)
	r := step4Router(tenant, actor, step4Handlers(t, app))

	stored := func(id string) map[string]any {
		t.Helper()
		var raw []byte
		if err := admin.QueryRow(ctx, `SELECT protocol_config FROM nodes WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rowVersion := 1
	patch := func(id, body string, want int) string {
		t.Helper()
		w := step3Do(t, ctx, r, http.MethodPatch, "/v1/nodes/"+id, `{"row_version":`+strconv.Itoa(rowVersion)+`,`+body+`}`)
		if w.Code != want {
			t.Fatalf("PATCH %s: status=%d want %d body=%s", body, w.Code, want, w.Body.String())
		}
		if w.Code == http.StatusOK {
			rowVersion++
		}
		return w.Body.String()
	}

	// 嵌套的 obfs.password：以前会被悄悄清空。响应仍然抹敏。
	body := patch(hy2, `"protocol_config":{"network":"udp","cert_path":"/etc/pandora/cert2.pem","key_path":"/etc/pandora/key.pem","obfs":{"type":"salamander"}}`, http.StatusOK)
	if strings.Contains(body, "fixture-obfs") {
		t.Fatalf("response leaked secret: %s", body)
	}
	got := stored(hy2)
	if got["cert_path"] != "/etc/pandora/cert2.pem" || got["obfs"].(map[string]any)["password"] != "fixture-obfs" {
		t.Fatalf("nested secret not kept: %+v", got)
	}
	// 只改名字、不带 protocol_config：原样不动。
	patch(hy2, `"name":"hy2-renamed"`, http.StatusOK)
	if stored(hy2)["obfs"].(map[string]any)["password"] != "fixture-obfs" {
		t.Fatalf("rename touched secret: %+v", stored(hy2))
	}
	// 显式给新值才覆盖。
	patch(hy2, `"protocol_config":{"network":"udp","cert_path":"/etc/pandora/cert2.pem","key_path":"/etc/pandora/key.pem","obfs":{"type":"salamander","password":"obfs-two"}}`, http.StatusOK)
	if stored(hy2)["obfs"].(map[string]any)["password"] != "obfs-two" {
		t.Fatalf("explicit secret not applied: %+v", stored(hy2))
	}

	// 顶层必填的 password：以前拿读接口的形状回写直接 422。
	rowVersion = 1
	patch(stls, `"protocol_config":{"network":"tcp","version":3,"server":"b.example.com:443","method":"aes-256-gcm","strict":true}`, http.StatusOK)
	if got := stored(stls); got["password"] != "fixture-outer" || got["server"] != "b.example.com:443" {
		t.Fatalf("top-level secret not kept: %+v", got)
	}
	// 换协议类型：旧密钥不属于 shadowsocks，带过去会被协议白名单拒成 422。
	patch(stls, `"node_type":"shadowsocks","protocol_config":{"cipher":"aes-256-gcm"}`, http.StatusOK)
	if got := stored(stls); got["password"] != nil || got["cipher"] != "aes-256-gcm" {
		t.Fatalf("secret leaked across node types: %+v", got)
	}
}

// R93：超时与重试次数越界回 422 带字段，不落库；合法值照常保存。
func TestPluginHookBoundsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "87000000-0000-4000-8000-000000000101"
		actor  = "87000000-0000-4000-8000-000000000111"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','hook-bounds-pg18','Hook Bounds','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@hook-bounds.invalid','Ops','active')`)
	h := step4Handlers(t, app)
	h.d.Plugin = plugin.New(app, h.d.Envelope, true)
	r := step4Router(tenant, actor, h)
	r.(*chi.Mux).Post("/v1/plugin-hooks", h.saveHook)

	count := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM plugin_hooks WHERE tenant_id=$1`, tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	save := func(timeout, attempts int) (int, string) {
		w := step3Do(t, ctx, r, http.MethodPost, "/v1/plugin-hooks",
			`{"code":"hook-bounds","name":"Bounds","enabled":false,"events":[],"endpoint_url":"https://hooks.example.com/in","timeout_ms":`+
				strconv.Itoa(timeout)+`,"max_attempts":`+strconv.Itoa(attempts)+`}`)
		return w.Code, w.Body.String()
	}
	for _, tc := range []struct {
		timeout, attempts int
		field             string
	}{{30001, 5, "timeout_ms"}, {499, 5, "timeout_ms"}, {5000, 11, "max_attempts"}, {5000, -1, "max_attempts"}} {
		code, body := save(tc.timeout, tc.attempts)
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"`+tc.field+`"`) {
			t.Fatalf("timeout=%d attempts=%d: status=%d body=%s", tc.timeout, tc.attempts, code, body)
		}
	}
	if n := count(); n != 0 {
		t.Fatalf("rejected saves wrote %d rows", n)
	}
	if code, body := save(30000, 10); code != http.StatusOK {
		t.Fatalf("boundary save: status=%d body=%s", code, body)
	}
	var timeout, attempts int
	if err := admin.QueryRow(ctx, `SELECT timeout_ms,max_attempts FROM plugin_hooks WHERE tenant_id=$1 AND code='hook-bounds'`, tenant).Scan(&timeout, &attempts); err != nil {
		t.Fatal(err)
	}
	if timeout != 30000 || attempts != 10 {
		t.Fatalf("saved timeout=%d attempts=%d", timeout, attempts)
	}
}
