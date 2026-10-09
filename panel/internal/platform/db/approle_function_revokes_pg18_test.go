package db_test

import (
	"fmt"
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

	// 签名交给库解析：先 to_regprocedure；它不认参数名与参数模式，认不出时再用与 GRANT 同一套语法的
	// COMMENT ON FUNCTION 在回滚掉的子事务里解析一次（找到的函数就是 GRANT / REVOKE 当时作用的那个）
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	resolve := func(sig string) (string, bool) {
		var oid *uint32
		if err := tx.QueryRow(ctx, `SELECT to_regprocedure($1)::oid`, sig).Scan(&oid); err != nil {
			t.Fatalf("to_regprocedure(%q): %v", sig, err)
		}
		if oid == nil {
			if _, err := tx.Exec(ctx, `SAVEPOINT pandora_probe`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, cerr := tx.Exec(ctx, `COMMENT ON FUNCTION `+sig+` IS 'pandora-app-role-exec-probe'`)
			if cerr == nil {
				if err := tx.QueryRow(ctx, `SELECT objoid FROM pg_catalog.pg_description
					WHERE classoid = 'pg_catalog.pg_proc'::regclass AND description = 'pandora-app-role-exec-probe'`).Scan(&oid); err != nil {
					t.Fatalf("read probe comment for %q: %v", sig, err)
				}
			}
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pandora_probe`); err != nil {
				t.Fatalf("rollback to savepoint: %v", err)
			}
			if oid == nil {
				return "", false
			}
		}
		var canonical string
		if err := tx.QueryRow(ctx, `SELECT $1::oid::regprocedure::text`, *oid).Scan(&canonical); err != nil {
			t.Fatalf("canonical name of %q: %v", sig, err)
		}
		return canonical, true
	}
	revoked, err := migrationRevokedFromApp(readMigrationUps(t), resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(revoked, "app.seed_tenant_defaults(uuid)") {
		t.Fatalf("migration scan lost app.seed_tenant_defaults(uuid); got %v", revoked)
	}

	var executable []string
	for _, sig := range revoked {
		var canExec bool
		if err := tx.QueryRow(ctx, `SELECT has_function_privilege('aegis_app', to_regprocedure($1), 'EXECUTE')`,
			sig).Scan(&canExec); err != nil {
			t.Fatalf("check %s: %v", sig, err)
		}
		if canExec {
			executable = append(executable, sig)
		}
	}
	if len(executable) > 0 {
		t.Fatalf("aegis_app can execute functions the migrations revoked from it (configure-app-role.sql grants them back): %v",
			executable)
	}
	t.Logf("checked %d revoked functions: %v", len(revoked), revoked)
}

// 不连库的解析：小写、去掉全部空白，只用来在单元测试里比对同一种写法
func textResolve(sig string) (string, bool) {
	return strings.ToLower(strings.Join(strings.Fields(sig), "")), true
}

// 不连库：扫描本身要认出已知的几个（收回的、收回后又授回的），扫描坏了 PG18 用例会在绿灯下什么也没核
func TestMigrationRevokedFromAppScan(t *testing.T) {
	revoked, err := migrationRevokedFromApp(readMigrationUps(t), textResolve)
	if err != nil {
		t.Fatal(err)
	}
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

// 不连库：签名里的空白收成单个空格交给解析（参数名、多词类型原样保留）；解析不出来的，
// 只有后面的迁移里 DROP 了同名函数才跳过，否则报错；format() 模板（含 %）不算签名
func TestMigrationRevokedFromAppScanRules(t *testing.T) {
	var seen []string
	record := func(sig string) (string, bool) {
		seen = append(seen, sig)
		if strings.HasPrefix(sig, "app.gone(") {
			return "", false
		}
		return sig, true
	}
	ups := []migrationUp{
		{"00001_a.sql", "REVOKE ALL ON FUNCTION app.f(p_tenant uuid,\n   p_at timestamp   with time zone) FROM PUBLIC, aegis_app;\n" +
			"REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;\n" +
			"DO $$ BEGIN EXECUTE format('REVOKE ALL ON FUNCTION %s FROM aegis_app;', v_sig); END $$;\n"},
		{"00002_b.sql", "DROP FUNCTION IF EXISTS app.gone(uuid);\n"},
	}
	revoked, err := migrationRevokedFromApp(ups, record)
	if err != nil {
		t.Fatalf("a revoke followed by a later DROP FUNCTION must be skipped: %v", err)
	}
	if want := "app.f(p_tenant uuid, p_at timestamp with time zone)"; !contains(revoked, want) || !contains(seen, want) {
		t.Fatalf("signature not normalized to single spaces: revoked %v, resolved %v", revoked, seen)
	}
	for _, sig := range seen {
		if strings.Contains(sig, "%") {
			t.Fatalf("a format() template was treated as a signature: %v", seen)
		}
	}
	if _, err := migrationRevokedFromApp(ups[:1], record); err == nil {
		t.Fatal("an unresolvable signature without a later DROP FUNCTION was skipped silently")
	}
	// DROP 在 REVOKE 之前不算
	early := []migrationUp{{"00001_a.sql", "DROP FUNCTION IF EXISTS app.gone(uuid);\n"}, {"00002_b.sql", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;\n"}}
	if _, err := migrationRevokedFromApp(early, record); err == nil {
		t.Fatal("a DROP FUNCTION before the revoke excused an unresolvable signature")
	}
}

var (
	functionPrivilegeStatement = regexp.MustCompile(
		`(?is)\b(REVOKE|GRANT)\s+(?:ALL(?:\s+PRIVILEGES)?|EXECUTE)\s+ON\s+FUNCTION\s+(.+?)\s+(FROM|TO)\s+([^;]+);`)
	dropFunctionStatement = regexp.MustCompile(`(?is)\bDROP\s+FUNCTION\s+(?:IF\s+EXISTS\s+)?([a-z_][a-z0-9_]*\.[a-z_][a-z0-9_]*)\s*\(`)
	gooseDown             = regexp.MustCompile(`(?m)^-- \+goose Down`)
)

type migrationUp struct{ name, up string }

// readMigrationUps 按文件名顺序读全部迁移的 Up 段
func readMigrationUps(t *testing.T) []migrationUp {
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
	var ups []migrationUp
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		up := string(body)
		if loc := gooseDown.FindStringIndex(up); loc != nil {
			up = up[:loc[0]]
		}
		ups = append(ups, migrationUp{filepath.Base(file), up})
	}
	return ups
}

// migrationRevokedFromApp 返回按迁移顺序最后一次对 aegis_app 是 REVOKE 的函数（resolve 给出的规范名）。
// 签名里的空白收成单个空格交给 resolve；resolve 认不出的，只有之后（后面的语句或迁移）DROP 了同名函数才跳过，
// 否则报错——免得新写法悄悄脱离覆盖。format() 模板（签名里有 %）是动态 SQL，静态扫不了，跳过
func migrationRevokedFromApp(ups []migrationUp, resolve func(string) (string, bool)) ([]string, error) {
	type event struct {
		pos       int // 全局位置：文件序号 << 32 | 文件内偏移
		verb, sig string
		name      string
	}
	type drop struct {
		pos  int
		name string
	}
	var events []event
	var drops []drop
	for i, m := range ups {
		for _, loc := range functionPrivilegeStatement.FindAllStringSubmatchIndex(m.up, -1) {
			verb := strings.ToUpper(m.up[loc[2]:loc[3]])
			functions, roles := m.up[loc[4]:loc[5]], m.up[loc[8]:loc[9]]
			if !hasRole(roles, "aegis_app") {
				continue
			}
			for _, sig := range splitTopLevel(functions) {
				sig = strings.Join(strings.Fields(sig), " ")
				if strings.Contains(sig, "%") {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(strings.SplitN(sig, "(", 2)[0]))
				events = append(events, event{i<<32 | loc[0], verb, sig, name})
			}
		}
		for _, loc := range dropFunctionStatement.FindAllStringSubmatchIndex(m.up, -1) {
			drops = append(drops, drop{i<<32 | loc[0], strings.ToLower(m.up[loc[2]:loc[3]])})
		}
	}
	last := map[string]string{}
	var unresolved []string
	for _, e := range events {
		key, ok := resolve(e.sig)
		if !ok {
			excused := false
			for _, d := range drops {
				if d.pos > e.pos && d.name == e.name {
					excused = true
					break
				}
			}
			if !excused {
				unresolved = append(unresolved, fmt.Sprintf("%s: %s %s", ups[e.pos>>32].name, e.verb, e.sig))
			}
			continue
		}
		last[key] = e.verb
	}
	if len(unresolved) > 0 {
		return nil, fmt.Errorf("function signatures in GRANT/REVOKE for aegis_app that cannot be resolved and are not dropped later: %v", unresolved)
	}
	var out []string
	for sig, verb := range last {
		if verb == "REVOKE" {
			out = append(out, sig)
		}
	}
	sort.Strings(out)
	return out, nil
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
