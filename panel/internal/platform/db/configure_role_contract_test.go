package db

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigureAppRoleClosesTemporarySearchPathSurface(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(root, "deploy", "configure-app-role.sql"))
	if err != nil {
		t.Fatalf("read configure-app-role.sql: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC, aegis_app",
		"ALTER ROLE aegis_app IN DATABASE %I SET search_path TO pg_catalog, public, pg_temp",
		"REVOKE CREATE ON SCHEMA public, app FROM PUBLIC, aegis_app",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("configure-app-role.sql is missing %q", required)
		}
	}
	if strings.Contains(normalized, "ALTER ROLE aegis_app SET search_path") {
		t.Fatal("search_path must be database-scoped; cluster-wide ALTER ROLE is forbidden")
	}
}

// 证据流水与带守卫触发器的表：DELETE 授权必须在脚本末尾收回，且排在把列级授权
// 提升回表级的那一段之后 —— 排在前面会被它重新放开（gift_card_redemptions 与
// traffic_reset_logs 就这样失效过）。
func TestConfigureAppRoleRevokesDeleteOnGuardedTablesLast(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(root, "deploy", "configure-app-role.sql"))
	if err != nil {
		t.Fatalf("read configure-app-role.sql: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(body)), " ")
	regrant := strings.LastIndex(normalized, "EXECUTE format('GRANT %s ON public.%I TO aegis_app'")
	if regrant < 0 {
		t.Fatal("table-level re-grant block not found")
	}
	for _, revoke := range []string{
		"REVOKE UPDATE, DELETE ON gift_card_redemptions FROM aegis_app;",
		"REVOKE UPDATE, DELETE ON traffic_reset_logs FROM aegis_app;",
		"REVOKE DELETE ON traffic_pack_grants FROM aegis_app;",
		"REVOKE DELETE ON gift_card_batches FROM aegis_app;",
	} {
		at := strings.LastIndex(normalized, revoke)
		if at < 0 || at < regrant {
			t.Errorf("%q must appear after the table-level re-grant block", revoke)
		}
	}
}
