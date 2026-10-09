package db_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 迁移里明确收回过 aegis_app 执行权的函数（多是 SECURITY DEFINER，只给迁移、调度或触发器用），跑过
// configure-app-role.sql（每次安装、升级、恢复都跑）之后，aegis_app 仍然不能执行。
//
// configure-app-role.sql 先对整个 app 模式 GRANT EXECUTE 给 aegis_app，再逐个收回；漏收一个，迁移里的
// REVOKE 就在每次 bootstrap 后失效（app.seed_tenant_defaults(uuid) 就这样被授回去过）。清单从迁移原文现扫：
// 按迁移顺序看每个函数最后一次是收回还是授回，最后是收回的都要核对。run-pg18-gates.sh 的 app_role_exec 域
// 先应用 configure-app-role.sql 再跑这个用例。
func TestAppRoleFunctionRevokesPG18(t *testing.T) {
	ctx, admin, _ := pg18test.Open(t, pg18test.Fixture{
		Domain:         "APP_ROLE_EXEC",
		DatabasePrefix: "pandora_app_role_exec_gate",
		MarkerTable:    "pandora_app_role_exec_test_marker",
		CommentTag:     "pandora-app-role-exec-pg18",
	})

	revoked := migrationRevokedFromApp(t)
	if !contains(revoked, "app.seed_tenant_defaults(uuid)") {
		t.Fatalf("migration scan lost app.seed_tenant_defaults(uuid); got %v", revoked)
	}

	var executable, checked []string
	for _, sig := range revoked {
		var exists, canExec bool
		if err := admin.QueryRow(ctx, `
			SELECT to_regprocedure($1) IS NOT NULL,
			       coalesce(has_function_privilege('aegis_app', to_regprocedure($1), 'EXECUTE'), false)`,
			sig).Scan(&exists, &canExec); err != nil {
			t.Fatalf("check %s: %v", sig, err)
		}
		if !exists {
			continue // 后来的迁移删掉了
		}
		checked = append(checked, sig)
		if canExec {
			executable = append(executable, sig)
		}
	}
	if !contains(checked, "app.seed_tenant_defaults(uuid)") {
		t.Fatal("app.seed_tenant_defaults(uuid) does not exist in the migrated database")
	}
	if len(executable) > 0 {
		t.Fatalf("aegis_app can execute functions the migrations revoked from it (configure-app-role.sql grants them back): %v",
			executable)
	}
	t.Logf("checked %d revoked functions: %v", len(checked), checked)
}

// 不连库：扫描本身要认出已知的几个（收回的、收回后又授回的），扫描坏了 PG18 用例会在绿灯下什么也没核
func TestMigrationRevokedFromAppScan(t *testing.T) {
	revoked := migrationRevokedFromApp(t)
	for _, want := range []string{
		"app.seed_tenant_defaults(uuid)",
		"app.guard_finalized_refund_ledger_entry()",
		"app.assert_refund_request(uuid,uuid)",
	} {
		if !contains(revoked, want) {
			t.Fatalf("scan lost %s: %v", want, revoked)
		}
	}
	// 00038 先 REVOKE ALL 再 GRANT EXECUTE 给 aegis_app：最后一次是授回，不在清单里
	for _, granted := range []string{
		"app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)",
		"app.idempotency_actor_scope(text,uuid)",
	} {
		if contains(revoked, granted) {
			t.Fatalf("scan treats %s as revoked although the migrations grant it back", granted)
		}
	}
}

var (
	functionPrivilegeStatement = regexp.MustCompile(
		`(?is)\b(REVOKE|GRANT)\s+(?:ALL(?:\s+PRIVILEGES)?|EXECUTE)\s+ON\s+FUNCTION\s+(.+?)\s+(FROM|TO)\s+([^;]+);`)
	plainSignature = regexp.MustCompile(`^[a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*\([a-z0-9_, \[\]]*\)$`)
	gooseDown      = regexp.MustCompile(`(?m)^-- \+goose Down`)
)

// migrationRevokedFromApp 返回按迁移顺序最后一次对 aegis_app 是 REVOKE 的函数签名（去空白、小写）。
func migrationRevokedFromApp(t *testing.T) []string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	dir := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "migrations"))
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("read migrations from %s: %v", dir, err)
	}
	sort.Strings(files)
	last := map[string]string{}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		up := string(body)
		if loc := gooseDown.FindStringIndex(up); loc != nil {
			up = up[:loc[0]]
		}
		for _, m := range functionPrivilegeStatement.FindAllStringSubmatch(up, -1) {
			verb, functions, roles := strings.ToUpper(m[1]), m[2], m[4]
			if !hasRole(roles, "aegis_app") {
				continue
			}
			for _, sig := range splitTopLevel(functions) {
				sig = strings.ToLower(strings.Join(strings.Fields(sig), ""))
				if !plainSignature.MatchString(sig) {
					continue // 动态 SQL 里的 %s 之类
				}
				last[sig] = verb
			}
		}
	}
	var out []string
	for sig, verb := range last {
		if verb == "REVOKE" {
			out = append(out, sig)
		}
	}
	sort.Strings(out)
	return out
}

func hasRole(list, role string) bool {
	for _, r := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(r), role) {
			return true
		}
	}
	return false
}

// splitTopLevel 按不在括号里的逗号切开「f(a,b), g()」这样的函数清单
func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
