// [INPUT]: 依赖 announcement_pg18_test.go 的 openAnnouncementPG18、step3_pg18_test.go 的 step3Seed / step3Do / step3Nodes、step4_pg18_test.go 的 step4Handlers / step4Router，依赖 node_admin.go、appearance.go、handlers.go（setSwitch）与 mail.go 的处理器，依赖 domain/notify 的 LoadSMTPConfig
// [OUTPUT]: 对外提供 TestNodePatchKeepsSecretsPG18、TestPluginHookBoundsPG18、TestTenantSeedDefaultsPG18
// [POS]: api/admin 的第 4 阶段后端三 PG18 测试：节点 PATCH 缺席的敏感键保留原值（R78）、钩子超时与重试次数越界回 422 且不落库（R93）、建租户触发器补种与 SMTP 密码 upsert（R94、R97）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// R78：读接口抹掉敏感键，前端拿抹过的配置只改一个普通字段再 PATCH 回来，
// 库里的密钥必须还在；显式给新值才覆盖；换协议类型不把旧密钥带过去。
func TestNodePatchKeepsSecretsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant = "8b000000-0000-4000-8000-000000000001"
		actor  = "8b000000-0000-4000-8000-000000000011"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','node-secret-pg18','Node Secret','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@node-secret.invalid','Ops','active')`)
	nodes := step3Nodes(t, ctx, admin, tenant, "8b000000-0000-4000-8000-", 2)
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
		tenant = "8b000000-0000-4000-8000-000000000101"
		actor  = "8b000000-0000-4000-8000-000000000111"
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

// R94 / R97 与线下渠道、通知模板：新建的租户由 tenants 的 AFTER INSERT 触发器
// 种下线下渠道、12 个内置模板与 8 个降级开关，和默认租户一样能用。
func TestTenantSeedDefaultsPG18(t *testing.T) {
	ctx, admin, app := openAnnouncementPG18(t)
	const (
		tenant  = "8b000000-0000-4000-8000-000000000201"
		actor   = "8b000000-0000-4000-8000-000000000211"
		viaApp  = "8b000000-0000-4000-8000-000000000221"
		scopeOf = "8b000000-0000-4000-8000-000000000231"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','seed-pg18','Seed','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@seed.invalid','Ops','active')`,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+scopeOf+`','seed-scope-pg18','Seed Scope','CNY')`)

	type seeded struct {
		offline, offlineSelectable     bool
		templates, switches, essential int
	}
	read := func(id string) seeded {
		t.Helper()
		var out seeded
		if err := admin.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM payment_providers WHERE tenant_id=$1 AND code='offline' AND enabled),
			       EXISTS (SELECT 1 FROM payment_providers WHERE tenant_id=$1 AND code='offline' AND accepting_new),
			       (SELECT count(*) FROM notification_templates WHERE tenant_id=$1 AND status='active'),
			       (SELECT count(*) FROM feature_switches WHERE tenant_id=$1 AND enabled),
			       (SELECT count(*) FROM feature_switches WHERE tenant_id=$1 AND essential)`, id).Scan(
			&out.offline, &out.offlineSelectable, &out.templates, &out.switches, &out.essential); err != nil {
			t.Fatal(err)
		}
		return out
	}
	want := seeded{offline: true, templates: 12, switches: 8, essential: 3}
	if got := read(tenant); got != want {
		t.Fatalf("seeded tenant=%+v want %+v", got, want)
	}
	// 默认租户经迁移补种后同样齐全（其余测试可能改过它的开关状态，只数行）；R102 删掉的三项不在
	var defaultRows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM feature_switches WHERE tenant_id='00000000-0000-7000-8000-000000000001'`).Scan(&defaultRows); err != nil || defaultRows != 8 {
		t.Fatalf("default tenant switch rows=%d err=%v", defaultRows, err)
	}
	if got := read("00000000-0000-7000-8000-000000000001"); got.templates < 12 || !got.offline || got.essential != 3 {
		t.Fatalf("default tenant=%+v", got)
	}
	var dropped int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM feature_switches WHERE code IN ('ops.bulk_export','ops.reports','node.autoscale')`).Scan(&dropped); err != nil || dropped != 0 {
		t.Fatalf("unwired switches left=%d err=%v", dropped, err)
	}

	// 应用角色建租户也有种子；seed 函数只经触发器进来，应用角色自己调不到；
	// 函数临时切过会话租户，事务里后续语句看到的仍是调用方自己的租户
	var restored string
	if err := app.InTx(ctx, platformdb.Scope{TenantID: scopeOf}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'seed-app-pg18','Seed App','CNY')`, viaApp); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT current_setting('app.tenant_id')`).Scan(&restored)
	}); err != nil {
		t.Fatalf("app role tenant insert: %v", err)
	}
	if restored != scopeOf {
		t.Fatalf("app.tenant_id after trigger=%q want %q", restored, scopeOf)
	}
	if got := read(viaApp); got != want {
		t.Fatalf("app-created tenant=%+v want %+v", got, want)
	}
	var appExec, publicExec bool
	if err := admin.QueryRow(ctx, `SELECT has_function_privilege('aegis_app','app.seed_tenant_defaults(uuid)','EXECUTE'),
		has_function_privilege('public','app.seed_tenant_defaults(uuid)','EXECUTE')`).Scan(&appExec, &publicExec); err != nil || appExec || publicExec {
		t.Fatalf("seed function executable by app=%v public=%v err=%v", appExec, publicExec, err)
	}

	// 新租户在后台切得动开关（R97），SMTP 密码行缺失时也写得进去（R94）
	h := step4Handlers(t, app)
	r := step4Router(tenant, actor, h).(*chi.Mux)
	r.Post("/v1/switches/{code}", h.setSwitch)
	r.Post("/v1/settings/mail", h.setMailSettings)
	if w := step3Do(t, ctx, r, http.MethodPost, "/v1/switches/marketing.giftcard.redeem", `{"enabled":false,"reason":"演练"}`); w.Code != http.StatusOK {
		t.Fatalf("toggle seeded switch: status=%d body=%s", w.Code, w.Body.String())
	}
	var redeemOn bool
	if err := admin.QueryRow(ctx, `SELECT enabled FROM feature_switches WHERE tenant_id=$1 AND code='marketing.giftcard.redeem'`, tenant).Scan(&redeemOn); err != nil || redeemOn {
		t.Fatalf("switch not toggled: enabled=%v err=%v", redeemOn, err)
	}
	step3Seed(t, ctx, admin, `DELETE FROM system_settings WHERE tenant_id='`+tenant+`' AND key='mail.smtp_password'`)
	body := `{"smtp_host":"smtp.example.test","smtp_port":465,"encryption":"ssl","smtp_username":"u","smtp_password":"fixture-smtp","from_address":"noreply@example.test","from_name":"Seed"}`
	if w := step3Do(t, ctx, r, http.MethodPost, "/v1/settings/mail", body); w.Code != http.StatusOK {
		t.Fatalf("save mail settings: status=%d body=%s", w.Code, w.Body.String())
	}
	cfg, err := notify.LoadSMTPConfig(ctx, app, h.d.Envelope, tenant)
	if err != nil || cfg.Password != "fixture-smtp" {
		t.Fatalf("smtp password after upsert=%q err=%v", cfg.Password, err)
	}
	var secret bool
	if err := admin.QueryRow(ctx, `SELECT is_secret FROM system_settings WHERE tenant_id=$1 AND key='mail.smtp_password'`, tenant).Scan(&secret); err != nil || !secret {
		t.Fatalf("upserted row is_secret=%v err=%v", secret, err)
	}
}
