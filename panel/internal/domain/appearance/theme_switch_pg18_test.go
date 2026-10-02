// [INPUT]: 依赖 service.go 的 SaveTheme（Create）/ ActivateTheme / DeleteTheme / ListThemes / Public、tokens.go 的 SiteNameTx，依赖一次性 PG18 库（run-pg18-gates.sh 的 appearance 域，夹具与 theme_pg18_test.go 相同）
// [OUTPUT]: 对外提供 TestThemeSwitchPG18
// [POS]: domain/appearance 多主题新建与切换的 PG18 集成测试：另存为不覆盖已有主题（409）、自定义主题可编辑、激活即换门户令牌与站点名（邮件 {{site}} 与发件人名同源）、生效中与内置主题删不掉、每个写操作一条审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestThemeSwitchPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_APPEARANCE_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_APPEARANCE_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_APPEARANCE_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_APPEARANCE_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_APPEARANCE_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("appearance PostgreSQL 18 fixture is not configured")
	}
	if fixture != "disposable-v1" || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_appearance_") ||
		!regexp.MustCompile(`^[a-z0-9]{32}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var database, marker string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_appearance_test_marker)`).Scan(&database, &version, &marker); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q", database, version, marker)
	}

	// 独立租户：从迁移种下的默认租户复制一份「默认 · 纸白」，不碰 TestPaperThemePG18 的数据
	const (
		tenant  = "75000000-0000-7000-8000-000000000001"
		decoy   = "75000000-0000-7000-8000-000000000002"
		seeded  = "00000000-0000-7000-8000-000000000001"
		actorID = "75000000-0000-7000-8000-000000000011"
	)
	for _, sql := range []string{
		`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES
			('` + tenant + `', 'theme-switch-pg18', 'Theme Switch PG18', 'CNY'),
			('` + decoy + `', 'theme-switch-decoy', 'Theme Switch Decoy', 'CNY')`,
		`INSERT INTO site_themes (tenant_id, code, name, is_builtin, is_active, tokens, branding)
		 SELECT '` + tenant + `', code, name, true, true, tokens, branding
		   FROM site_themes WHERE tenant_id = '` + seeded + `' AND code = 'paper'`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	svc := New(app)
	siteName := func(tid string) string {
		t.Helper()
		var name string
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tid}, func(tx pgx.Tx) error {
			var err error
			name, err = SiteNameTx(ctx, tx, tid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return name
	}
	isCode := func(err error, code httpx.Code) bool {
		var he *httpx.Error
		return errors.As(err, &he) && he.Code == code
	}

	// 1) 新建：从内置主题另存为，带品牌与两组令牌
	night := SaveThemeInput{
		Code: "night-sea", Name: "夜海", Create: true, ActorID: actorID,
		Tokens:   json.RawMessage(`{"light":{"--brand":"#2b6cb9","--bg":"#f0f4f8"},"dark":{"--brand":"#5b9be4"}}`),
		Branding: json.RawMessage(`{"site_name":"夜海加速","tagline":"一路畅通"}`),
	}
	if _, err := svc.SaveTheme(ctx, tenant, night); err != nil {
		t.Fatalf("create custom theme: %v", err)
	}
	// 同 code 再「新建」：409，原主题不被覆盖
	dup := night
	dup.Name, dup.Branding = "冒名", json.RawMessage(`{"site_name":"冒名"}`)
	if _, err := svc.SaveTheme(ctx, tenant, dup); !isCode(err, httpx.CodeConflict) {
		t.Fatalf("create over an existing code err=%v, want 409", err)
	}
	// 以内置主题的 code「新建」同样 422（内置不可改），不是覆盖
	if _, err := svc.SaveTheme(ctx, tenant, SaveThemeInput{Code: "paper", Name: "x", Create: true,
		Branding: json.RawMessage(`{"site_name":"x"}`), ActorID: actorID}); !isCode(err, httpx.CodeValidationFailed) {
		t.Fatalf("create over builtin err=%v, want 422", err)
	}
	// 编辑（Create=false）照常 upsert
	edit := night
	edit.Create, edit.Name = false, "夜海 · 改"
	if _, err := svc.SaveTheme(ctx, tenant, edit); err != nil {
		t.Fatalf("edit custom theme: %v", err)
	}
	list, err := svc.ListThemes(ctx, tenant)
	if err != nil || len(list) != 2 || list[0].Code != "paper" || !list[0].IsActive || list[1].Name != "夜海 · 改" || list[1].IsActive {
		t.Fatalf("themes after create/edit = %+v err=%v", list, err)
	}
	t.Log("marker=appearance_pg18_create_no_overwrite_ok")

	// 2) 激活：门户拿到新令牌与新站点名，SiteNameTx（邮件 {{site}} 与发件人名）随之改变
	if siteName(tenant) != "Pandora" {
		t.Fatalf("site name before switch = %q", siteName(tenant))
	}
	if err := svc.ActivateTheme(ctx, tenant, "night-sea", actorID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	pub, err := svc.Public(ctx, tenant)
	if err != nil || pub.Theme == nil || pub.Theme.Code != "night-sea" {
		t.Fatalf("public after switch = %+v err=%v", pub, err)
	}
	var groups map[string]map[string]string
	var brand map[string]string
	_ = json.Unmarshal(pub.Theme.Tokens, &groups)
	_ = json.Unmarshal(pub.Theme.Branding, &brand)
	if groups["light"]["--brand"] != "#2b6cb9" || groups["dark"]["--brand"] != "#5b9be4" ||
		brand["site_name"] != "夜海加速" || brand["tagline"] != "一路畅通" {
		t.Fatalf("public tokens=%v branding=%v", groups, brand)
	}
	if got := siteName(tenant); got != "夜海加速" {
		t.Fatalf("site name after switch = %q", got)
	}
	if got := siteName(decoy); got != "Pandora" {
		t.Fatalf("decoy tenant site name = %q, switch leaked across tenants", got)
	}
	if _, err := svc.Public(ctx, decoy); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM site_themes WHERE tenant_id=$1 AND is_active`, tenant).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active themes = %d err=%v", active, err)
	}
	if err := svc.ActivateTheme(ctx, tenant, "no-such", actorID); !isCode(err, httpx.CodeNotFound) {
		t.Fatalf("activate unknown err=%v, want 404", err)
	}
	if got := siteName(tenant); got != "夜海加速" {
		t.Fatalf("failed activation must roll back, site name = %q", got)
	}
	t.Log("marker=appearance_pg18_switch_site_name_ok")

	// 3) 删除：生效中的、内置的都删不掉；切回内置后自定义主题可删
	if err := svc.DeleteTheme(ctx, tenant, "night-sea", actorID); !isCode(err, httpx.CodeValidationFailed) {
		t.Fatalf("delete active err=%v, want 422", err)
	}
	if err := svc.ActivateTheme(ctx, tenant, "paper", actorID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTheme(ctx, tenant, "paper", actorID); !isCode(err, httpx.CodeValidationFailed) {
		t.Fatalf("delete builtin err=%v, want 422", err)
	}
	if err := svc.DeleteTheme(ctx, tenant, "night-sea", actorID); err != nil {
		t.Fatalf("delete inactive custom: %v", err)
	}
	if got := siteName(tenant); got != "Pandora" {
		t.Fatalf("site name after switching back = %q", got)
	}
	t.Log("marker=appearance_pg18_delete_guards_ok")

	// 4) 审计：保存 2（新建、编辑）、激活 2、删除 1；被拒的写不留审计
	var actions []string
	if err := admin.QueryRow(ctx, `SELECT array_agg(action ORDER BY occurred_at, id) FROM audit_events
		WHERE tenant_id = $1 AND action LIKE 'appearance.theme.%'`, tenant).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	want := "appearance.theme.save,appearance.theme.save,appearance.theme.activate,appearance.theme.activate,appearance.theme.delete"
	if strings.Join(actions, ",") != want {
		t.Fatalf("theme audits = %v\nwant %s", actions, want)
	}
	t.Log("marker=appearance_pg18_audit_ok")
}
