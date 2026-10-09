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
	// COMMENT ON FUNCTION / PROCEDURE / ROUTINE（kind 与迁移里的写法一致）在回滚掉的子事务里解析一次（找到的函数就是 GRANT / REVOKE 当时作用的那个）
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	resolve := func(kind, sig string) (string, bool) {
		var oid *uint32
		if err := tx.QueryRow(ctx, `SELECT to_regprocedure($1)::oid`, sig).Scan(&oid); err != nil {
			t.Fatalf("to_regprocedure(%q): %v", sig, err)
		}
		if oid == nil {
			if _, err := tx.Exec(ctx, `SAVEPOINT pandora_probe`); err != nil {
				t.Fatalf("savepoint: %v", err)
			}
			_, cerr := tx.Exec(ctx, `COMMENT ON `+kind+` `+sig+` IS 'pandora-app-role-exec-probe'`)
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
func textResolve(_, sig string) (string, bool) {
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
	record := func(_, sig string) (string, bool) {
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

// 不连库：K4 的各种写法不能悄悄跳过。每个 red 用例都必须报错，green 用例必须不报错
func TestMigrationRevokedFromAppScanBlindSpots(t *testing.T) {
	// 名字以 app.gone 开头的签名库里认不出，其余都认得
	resolve := func(_, sig string) (string, bool) {
		if strings.HasPrefix(sig, "app.gone") {
			return "", false
		}
		return strings.ToLower(strings.Join(strings.Fields(sig), "")), true
	}
	red := []struct{ name, up, later string }{
		{"on all functions in schema", "REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM aegis_app;", ""},
		{"on all routines in schema", "REVOKE EXECUTE ON ALL ROUTINES IN SCHEMA app FROM aegis_app;", ""},
		{"on all procedures in schema", "REVOKE ALL ON ALL PROCEDURES IN SCHEMA app FROM aegis_app;", ""},
		{"bulk grant after a per-function revoke", "REVOKE ALL ON FUNCTION app.ok(uuid) FROM aegis_app;\nGRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO aegis_app;", ""},
		{"bulk revoke inside EXECUTE literal", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM aegis_app'; END $$;", ""},
		{"function privilege not parsed as a plain signature", "REVOKE USAGE ON FUNCTION app.f(uuid) FROM aegis_app;", ""},
		{"procedure unresolvable", "REVOKE ALL ON PROCEDURE app.gone_p(uuid) FROM aegis_app;", ""},
		{"routine unresolvable", "REVOKE ALL ON ROUTINE app.gone_r(uuid) FROM aegis_app;", ""},
		{"quoted role unresolvable", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM \"aegis_app\";", ""},
		{"EXECUTE literal unresolvable", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON FUNCTION app.gone2(uuid) FROM aegis_app'; END $$;", ""},
		{"DROP only in line comment", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "-- DROP FUNCTION app.gone(uuid);\n"},
		{"DROP only in block comment", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "/* DROP FUNCTION app.gone(uuid); */\n"},
		{"DROP with another signature", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION IF EXISTS app.gone(text, uuid);\n"},
		{"DROP of another function", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION app.gone_other(uuid);\n"},
	}
	for _, c := range red {
		ups := []migrationUp{{"00001_a.sql", c.up + "\n"}}
		if c.later != "" {
			ups = append(ups, migrationUp{"00002_b.sql", c.later})
		}
		if _, err := migrationRevokedFromApp(ups, resolve); err == nil {
			t.Errorf("%s: skipped silently, want an error", c.name)
		}
	}

	green := []struct {
		name, up, later string
		want            []string // 必须在清单里
	}{
		{"quoted role resolvable", "REVOKE ALL ON FUNCTION app.q(uuid) FROM \"aegis_app\";", "", []string{"app.q(uuid)"}},
		{"upper-case role", "REVOKE ALL ON FUNCTION app.q(uuid) FROM AEGIS_APP;", "", []string{"app.q(uuid)"}},
		{"procedure resolvable", "REVOKE ALL ON PROCEDURE app.p(uuid) FROM aegis_app;", "", []string{"app.p(uuid)"}},
		{"routine resolvable", "REVOKE ALL ON ROUTINE app.r(uuid) FROM aegis_app;", "", []string{"app.r(uuid)"}},
		{"EXECUTE literal resolvable", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON FUNCTION app.e(uuid) FROM aegis_app'; END $$;", "", []string{"app.e(uuid)"}},
		{"bulk grant before any revoke", "GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO aegis_app;\nREVOKE ALL ON FUNCTION app.k(uuid) FROM aegis_app;", "", []string{"app.k(uuid)"}},
		{"same-signature DROP, case and spacing differ", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION IF EXISTS app.GONE( UUID );\n", nil},
		{"DROP without argument list excuses by name", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION app.gone;\n", nil},
		{"DROP in a list", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION app.x(int), app.gone(uuid) CASCADE;\n", nil},
		{"table, sequence and schema grants", "GRANT SELECT ON TABLE app.t TO aegis_app;\nGRANT USAGE ON SCHEMA app TO aegis_app;\n" +
			"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO aegis_app;\nREVOKE ALL ON goose_db_version FROM aegis_app;", "", nil},
		{"statements only in comments", "-- REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM aegis_app;\n/* REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app; */", "", nil},
		{"format template stays skipped", "DO $$ BEGIN EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM aegis_app', s); END $$;", "", nil},
	}
	for _, c := range green {
		ups := []migrationUp{{"00001_a.sql", c.up + "\n"}}
		if c.later != "" {
			ups = append(ups, migrationUp{"00002_b.sql", c.later})
		}
		got, err := migrationRevokedFromApp(ups, resolve)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		for _, w := range c.want {
			if !contains(got, w) {
				t.Errorf("%s: %s missing from %v", c.name, w, got)
			}
		}
	}
}

var (
	// 一条 REVOKE / GRANT 的起点；语句到分号、单引号（EXECUTE '…' 字面量的结尾）或美元引号为止
	privilegeStart = regexp.MustCompile(`(?i)\b(?:REVOKE|GRANT)\b`)
	// 单个函数 / 存储过程 / 例程的权限语句：REVOKE|GRANT [GRANT OPTION FOR] ALL|EXECUTE ON FUNCTION|PROCEDURE|ROUTINE 签名清单 FROM|TO 角色
	functionPrivilegeStatement = regexp.MustCompile(
		`(?is)^(REVOKE|GRANT)\s+(?:GRANT\s+OPTION\s+FOR\s+)?(?:ALL(?:\s+PRIVILEGES)?|EXECUTE)\s+ON\s+(FUNCTION|PROCEDURE|ROUTINE)\s+(.+?)\s+(FROM|TO)\s+(.+)$`)
	// 函数类对象的写法：ON FUNCTION|PROCEDURE|ROUTINE 与 ON ALL FUNCTIONS|PROCEDURES|ROUTINES IN SCHEMA；
	// 表、序列、模式的授权不在内，ALTER DEFAULT PRIVILEGES 的 ON FUNCTIONS 只管以后新建的对象，也不在内
	functionObjectKind  = regexp.MustCompile(`(?is)\bON\s+(?:FUNCTION|PROCEDURE|ROUTINE|ALL\s+(?:FUNCTION|PROCEDURE|ROUTINE)S\s+IN\s+SCHEMA)\b`)
	bulkFunctionPrivlge = regexp.MustCompile(`(?is)^(REVOKE|GRANT)\b.*?\bON\s+ALL\s+(?:FUNCTION|PROCEDURE|ROUTINE)S\s+IN\s+SCHEMA\b`)
	dropFunctionStmt    = regexp.MustCompile(`(?is)\bDROP\s+(?:FUNCTION|PROCEDURE|ROUTINE)\s+(?:IF\s+EXISTS\s+)?`)
	identToken          = regexp.MustCompile(`"(?:[^"]|"")*"|[A-Za-z_][A-Za-z0-9_$]*`)
	gooseDown           = regexp.MustCompile(`(?m)^-- \+goose Down`)
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

// stripSQLComments 去掉 -- 行注释和（可嵌套的）/* */ 块注释，单引号字符串与双引号标识符里的原样保留
// （EXECUTE '…' 里的静态语句仍要扫）。行注释留下换行，块注释换成一个空格
func stripSQLComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c { // 连写两个引号是转义
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			depth := 0
			for i < len(s) {
				if strings.HasPrefix(s[i:], "/*") {
					depth++
					i += 2
				} else if strings.HasPrefix(s[i:], "*/") {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// statementEnd 返回从 from 起这条语句的结尾：分号、单引号（字面量结尾）、美元引号或文本尾
func statementEnd(s string, from int) int {
	if k := strings.IndexAny(s[from:], ";'$"); k >= 0 {
		return from + k
	}
	return len(s)
}

// mentionsRole 看文本里有没有标识符 role：不加引号的不分大小写，加引号的去引号后精确比较
func mentionsRole(text, role string) bool {
	for _, tok := range identToken.FindAllString(text, -1) {
		if strings.HasPrefix(tok, `"`) {
			if strings.ReplaceAll(tok[1:len(tok)-1], `""`, `"`) == role {
				return true
			}
		} else if strings.EqualFold(tok, role) {
			return true
		}
	}
	return false
}

// normalizeSig 小写并去掉全部空白，只用来比对 DROP 与 REVOKE 写的是不是同一个签名
func normalizeSig(sig string) string {
	return strings.ToLower(strings.Join(strings.Fields(sig), ""))
}

// migrationRevokedFromApp 返回按迁移顺序最后一次对 aegis_app 是 REVOKE 的函数（resolve 给出的规范名；
// kind 是迁移里写的 FUNCTION / PROCEDURE / ROUTINE）。先去掉注释，再扫每条 REVOKE / GRANT（含 EXECUTE '…'
// 里的静态字面量，角色可带双引号）。签名里的空白收成单个空格交给 resolve。
//
// 不能静默跳过的写法一律报错，免得新写法悄悄脱离覆盖：
//   - 提到 aegis_app、对象是函数类、却不是上面能解析的单个签名（ON ALL FUNCTIONS / PROCEDURES / ROUTINES IN SCHEMA 等）。
//     唯一的放行：第一个 per-function REVOKE 之前的 GRANT ... ON ALL FUNCTIONS IN SCHEMA（00011 的基线授权，
//     那时还没有可被它授回的收回）
//   - resolve 认不出的签名，除非之后（后面的语句或迁移）有 DROP FUNCTION / PROCEDURE / ROUTINE 同名同参数类型
//     （空白与大小写不计）；DROP 没写参数列表时只按名字放行
//
// format() 模板（语句里有 %）是动态 SQL，静态扫不了，跳过
func migrationRevokedFromApp(ups []migrationUp, resolve func(kind, sig string) (string, bool)) ([]string, error) {
	type event struct {
		pos             int // 全局位置：文件序号 << 32 | 文件内偏移
		verb, kind, sig string
		name, norm      string
		bulk            bool
	}
	type drop struct {
		pos        int
		name, norm string
		hasArgs    bool
	}
	var events []event
	var drops []drop
	var problems []string
	for i, m := range ups {
		text := stripSQLComments(m.up)
		for from := 0; ; {
			loc := privilegeStart.FindStringIndex(text[from:])
			if loc == nil {
				break
			}
			start, matchEnd := from+loc[0], from+loc[1]
			end := statementEnd(text, matchEnd)
			from = end
			stmt := strings.Join(strings.Fields(text[start:end]), " ")
			if strings.Contains(stmt, "%") || !mentionsRole(stmt, "aegis_app") || !functionObjectKind.MatchString(stmt) {
				continue
			}
			pos := i<<32 | start
			if bulkFunctionPrivlge.MatchString(stmt) {
				events = append(events, event{pos: pos, verb: strings.ToUpper(strings.Fields(stmt)[0]), sig: stmt, bulk: true})
				continue
			}
			sm := functionPrivilegeStatement.FindStringSubmatch(stmt)
			if sm == nil || !mentionsRole(sm[5], "aegis_app") {
				problems = append(problems, fmt.Sprintf("%s: unsupported function privilege statement: %s", m.name, stmt))
				continue
			}
			for _, sig := range splitTopLevel(sm[3]) {
				sig = strings.Join(strings.Fields(sig), " ")
				name := strings.ToLower(strings.TrimSpace(strings.SplitN(sig, "(", 2)[0]))
				events = append(events, event{pos: pos, verb: strings.ToUpper(sm[1]), kind: strings.ToUpper(sm[2]), sig: sig, name: name, norm: normalizeSig(sig)})
			}
		}
		for from := 0; ; {
			loc := dropFunctionStmt.FindStringIndex(text[from:])
			if loc == nil {
				break
			}
			start, matchEnd := from+loc[0], from+loc[1]
			end := statementEnd(text, matchEnd)
			list := strings.Join(strings.Fields(text[matchEnd:end]), " ")
			from = end
			for _, item := range splitTopLevel(list) {
				item = strings.TrimSpace(item)
				for _, tail := range []string{" CASCADE", " RESTRICT"} {
					if len(item) > len(tail) && strings.EqualFold(item[len(item)-len(tail):], tail) {
						item = strings.TrimSpace(item[:len(item)-len(tail)])
					}
				}
				name := strings.ToLower(strings.TrimSpace(strings.SplitN(item, "(", 2)[0]))
				if name == "" {
					continue
				}
				drops = append(drops, drop{i<<32 | start, name, normalizeSig(item), strings.Contains(item, "(")})
			}
		}
	}
	last := map[string]string{}
	seenRevoke := false
	for _, e := range events {
		if e.bulk {
			if e.verb == "GRANT" && !seenRevoke {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s: bulk function privilege cannot be checked per function: %s", ups[e.pos>>32].name, e.sig))
			continue
		}
		if e.verb == "REVOKE" {
			seenRevoke = true
		}
		key, ok := resolve(e.kind, e.sig)
		if !ok {
			excused := false
			for _, d := range drops {
				if d.pos > e.pos && d.name == e.name && (!d.hasArgs || d.norm == e.norm) {
					excused = true
					break
				}
			}
			if !excused {
				problems = append(problems, fmt.Sprintf("%s: %s %s cannot be resolved and is not dropped later", ups[e.pos>>32].name, e.verb, e.sig))
			}
			continue
		}
		last[key] = e.verb
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("GRANT/REVOKE for aegis_app the scan cannot judge: %v", problems)
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
