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
		"REVOKE DELETE, TRUNCATE ON user_generation_jobs FROM aegis_app;",
		"REVOKE UPDATE, DELETE, TRUNCATE ON traffic_pack_transfers FROM aegis_app;",
		"REVOKE UPDATE, TRUNCATE ON certificate_versions FROM aegis_app;",
		"REVOKE UPDATE, DELETE, TRUNCATE ON certificate_issuances FROM aegis_app;",
	} {
		at := strings.LastIndex(normalized, revoke)
		if at < 0 || at < regrant {
			t.Errorf("%q must appear after the table-level re-grant block", revoke)
		}
	}
}

// 运行角色的查询护栏：关 JIT、语句超时低于网关与 nginx 的超时；与 search_path 一样
// 按库设置，不许集群级 ALTER ROLE（一次性预检库与源库共享集群角色）。
// install.sh 装到 conf.d 的 postgresql-pandora.conf 另对整个实例关 JIT，两处一起钉住。
func TestConfigureAppRolePinsQueryGuards(t *testing.T) {
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
		"'ALTER ROLE aegis_app IN DATABASE %I SET jit = off', v_database",
		"'ALTER ROLE aegis_app IN DATABASE %I SET statement_timeout = %L', v_database, '15s'",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("configure-app-role.sql is missing %q", required)
		}
	}
	for _, forbidden := range []string{"ALTER ROLE aegis_app SET jit", "ALTER ROLE aegis_app SET statement_timeout"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("%q must be database-scoped", forbidden)
		}
	}
	conf, err := os.ReadFile(filepath.Join(root, "deploy", "postgresql-pandora.conf"))
	if err != nil {
		t.Fatalf("read postgresql-pandora.conf: %v", err)
	}
	if got, ok := lastPostgresSetting(string(conf), "jit"); !ok || got != "off" {
		t.Fatalf("postgresql-pandora.conf must turn JIT off for the whole instance with an active jit = off line (last jit setting: %q, present: %v)", got, ok)
	}
}

// lastPostgresSetting 按 postgresql.conf 的写法取某个参数最后一次生效的值：去掉 # 之后的注释，
// 「名字 = 值」或「名字 值」，值两侧的单引号去掉；同名参数后写的压过先写的。没写过返回 false。
func lastPostgresSetting(conf, name string) (string, bool) {
	value, found := "", false
	for _, line := range strings.Split(conf, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			key, rest, ok = strings.Cut(line, " ")
		}
		if !ok || !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		value = strings.ToLower(strings.Trim(strings.TrimSpace(rest), "'"))
		found = true
	}
	return value, found
}
