// [INPUT]: 依赖 service.go 的 Public / SaveTheme、tokens.go 的 SiteNameTx 与 DesignTokenKeys，读取 migrations/00075 的 Down 段，依赖一次性 PG18 库（run-pg18-gates.sh 的 appearance 域）
// [OUTPUT]: 对外提供 TestPaperThemePG18
// [POS]: domain/appearance 的 PG18 集成测试：迁移后的主题状态、门户只拿到白名单 token、内置主题不可改、Down 能恢复旧内置主题
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestPaperThemePG18(t *testing.T) {
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
	var database, marker, comment string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_appearance_test_marker), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`).Scan(&database, &version, &marker, &comment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID || comment != "pandora-appearance-pg18:"+runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q", database, version, marker, comment)
	}

	// 迁移时就存在的默认租户：只剩 paper 一个内置主题，且它在生效
	const tenant = "00000000-0000-7000-8000-000000000001"
	var builtins []string
	if err := admin.QueryRow(ctx, `SELECT array_agg(code ORDER BY code) FROM site_themes WHERE tenant_id=$1 AND is_builtin`,
		tenant).Scan(&builtins); err != nil {
		t.Fatal(err)
	}
	if strings.Join(builtins, ",") != "paper" {
		t.Fatalf("builtin themes after 00075 = %v, want only paper", builtins)
	}

	// 门户：tokens 只有 light/dark 两组、键全在白名单里；custom_css 不下发。
	// 先往库里塞一段旧 custom_css，证明读路径确实把它挡住了。
	if _, err := admin.Exec(ctx, `UPDATE site_themes SET custom_css='body{display:none}' WHERE tenant_id=$1 AND code='paper'`, tenant); err != nil {
		t.Fatal(err)
	}
	svc := New(app)
	pub, err := svc.Public(ctx, tenant)
	if err != nil || pub.Theme == nil {
		t.Fatalf("public appearance: theme=%v err=%v", pub, err)
	}
	var groups map[string]map[string]string
	if err := json.Unmarshal(pub.Theme.Tokens, &groups); err != nil {
		t.Fatal(err)
	}
	if pub.Theme.Code != "paper" || pub.Theme.CustomCSS != "" || len(groups) != 2 {
		t.Fatalf("public theme code=%s css=%q groups=%d", pub.Theme.Code, pub.Theme.CustomCSS, len(groups))
	}
	for _, g := range TokenGroups {
		if len(groups[g]) != len(DesignTokenKeys) {
			t.Fatalf("public %s tokens = %d, want %d", g, len(groups[g]), len(DesignTokenKeys))
		}
		for k := range groups[g] {
			if !designTokenSet[k] {
				t.Fatalf("public %s token %q is off the whitelist", g, k)
			}
		}
	}

	// 站点名：来自生效主题
	var site string
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
		var err error
		site, err = SiteNameTx(ctx, tx, tenant)
		return err
	}); err != nil || site != "Pandora" {
		t.Fatalf("site name = %q err=%v", site, err)
	}

	// 内置主题不能原地改；合法的自定义主题照常能存
	_, err = svc.SaveTheme(ctx, tenant, SaveThemeInput{Code: "paper", Name: "改名", ActorID: tenant})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
		t.Fatalf("overwriting the builtin paper theme must be a 422, got %v", err)
	}
	if _, err := svc.SaveTheme(ctx, tenant, SaveThemeInput{
		Code: "night-ink", Name: "夜墨", ActorID: "00000000-0000-7000-8000-00000000abcd",
		Tokens: json.RawMessage(`{"dark":{"--brand":"#e46e52"}}`)}); err != nil {
		t.Fatalf("save custom theme: %v", err)
	}

	// Down：在一个回滚的事务里执行 00075 的 Down 段，六个旧内置主题原样回来，
	// 没有主题生效时 default 生效
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00075_theme_paper_white.sql"))
	if err != nil {
		t.Fatal(err)
	}
	down := string(raw)[strings.Index(string(raw), "-- +goose Down"):]
	down = strings.NewReplacer("-- +goose StatementBegin", "", "-- +goose StatementEnd", "").Replace(down)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("run 00075 Down: %v", err)
	}
	var restored []string
	var active string
	if err := tx.QueryRow(ctx, `SELECT array_agg(code ORDER BY code) FROM site_themes WHERE tenant_id=$1 AND is_builtin`,
		tenant).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT code FROM site_themes WHERE tenant_id=$1 AND is_active`, tenant).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if strings.Join(restored, ",") != "aurora,default,midnight,stellar,stellar-dark,stellar-light" || active != "default" {
		t.Fatalf("after Down builtins=%v active=%s", restored, active)
	}
	var brand string
	if err := tx.QueryRow(ctx, `SELECT tokens->>'brand' FROM site_themes WHERE tenant_id=$1 AND code='default'`, tenant).Scan(&brand); err != nil || brand != "#6d5efc" {
		t.Fatalf("restored default brand=%q err=%v", brand, err)
	}
}
