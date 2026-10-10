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
	"unicode"

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
// 只有后面的迁移里 DROP 了同名函数才跳过，否则报错（含 % 的函数类语句另判红，见 BlindSpots）
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
			"REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;\n"},
		{"00002_b.sql", "DROP FUNCTION IF EXISTS app.gone(uuid);\n"},
	}
	revoked, err := migrationRevokedFromApp(ups, record)
	if err != nil {
		t.Fatalf("a revoke followed by a later DROP FUNCTION must be skipped: %v", err)
	}
	if want := "app.f(p_tenant uuid, p_at timestamp with time zone)"; !contains(revoked, want) || !contains(seen, want) {
		t.Fatalf("signature not normalized to single spaces: revoked %v, resolved %v", revoked, seen)
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
		// J8：函数类语句带 % 或拼接，静态扫不了，判红而不是跳过
		{"format template function revoke", "DO $$ BEGIN EXECUTE format('REVOKE ALL ON FUNCTION %s FROM aegis_app', v_sig); END $$;", ""},
		{"format template bulk revoke", "DO $$ BEGIN EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM aegis_app', s); END $$;", ""},
		{"format template function grant", "DO $$ BEGIN EXECUTE format('GRANT EXECUTE ON FUNCTION %s TO aegis_app', v_sig); END $$;", ""},
		// J9：'…' || '…' 拼接把函数 REVOKE 切成碎片
		{"concat splits after signature keyword", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON FUNCTION ' || 'app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat splits before role", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON FUNCTION app.x(uuid) FROM ' || 'aegis_app'; END $$;", ""},
		{"concat splits inside privilege keywords", "DO $$ BEGIN EXECUTE 'REVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		// J10：字符串里的 -- 和 /* 不是注释，后面真的 REVOKE 必须还看得见
		{"dollar-quoted string hides line comment", "COMMENT ON TABLE t IS $c$-- x$c$; REVOKE ALL ON FUNCTION app.gone3(uuid) FROM aegis_app;", ""},
		{"dollar-quoted string hides block comment", "COMMENT ON TABLE t IS $c$/* x$c$; REVOKE ALL ON FUNCTION app.gone3(uuid) FROM aegis_app;", ""},
		{"E string with escaped quote hides line comment", "COMMENT ON TABLE t IS E'a\\'b -- x'; REVOKE ALL ON FUNCTION app.gone3(uuid) FROM aegis_app;", ""},
		// J11：DROP 只有出现在语句开头才算
		{"ALTER EXTENSION DROP FUNCTION", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "ALTER EXTENSION e DROP FUNCTION app.gone(uuid);\n"},
		{"DROP text inside a string literal", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DO $$ BEGIN RAISE NOTICE 'DROP FUNCTION app.gone(uuid)'; END $$;\n"},
		// J12：美元引号作为语句结尾；statementEnd 若只认分号，这条会被漏掉
		{"EXECUTE dollar-quoted literal", "DO $$ BEGIN EXECUTE $q$REVOKE ALL ON FUNCTION app.gone4(uuid) FROM aegis_app$q$; END $$;", ""},
		{"DROP of another function", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DROP FUNCTION app.gone_other(uuid);\n"},
		// I3：函数类语句带 %，角色经 format 参数传进来也判红（不看角色）
		{"format template with the role as a parameter", "DO $$ BEGIN EXECUTE format('REVOKE ALL ON FUNCTION %s FROM %I', v, 'aegis_app'); END $$;", ""},
		// I11①：字符串里的「; DROP FUNCTION」不是语句开头
		{"DROP after a semicolon inside a string", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "DO $$ BEGIN RAISE NOTICE 'x; DROP FUNCTION app.gone(uuid)'; END $$;\n"},
		// N1：GRANT/REVOKE 不是字面量第一个词时，函数类拼接仍判红（碎片里没有角色也不能悄悄跳过）
		{"function concat after another statement in the literal", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; REVOKE ALL ON FUNCTION ' || v || ' FROM aegis_app'; END $$;", ""},
		{"function concat with the role in a later fragment", "DO $$ BEGIN EXECUTE 'SELECT 1; GRANT EXECUTE ON FUNCTION ' || v || ' TO ' || r; END $$;", ""},
		{"procedure concat after a prefix", "DO $$ BEGIN EXECUTE 'SET LOCAL y = 2; REVOKE ALL ON PROCEDURE ' || v || ' FROM aegis_app'; END $$;", ""},
		// G1：字面量里先有别的语句，并且在对象类型之前切开（碎片里还没写出 ON 对象）
		{"concat before the object kind after a prefix", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; REVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat after ALL after a prefix", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; REVOKE ALL ' || 'ON FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat right after the verb after a prefix", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; REVOKE ' || 'ALL ON FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		// F1：字面量里 REVOKE 前面隔着注释或 E 串转义，仍是语句开头
		{"concat after a block comment at the literal start", "DO $$ BEGIN EXECUTE '/* c */ REVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat after a semicolon and a block comment", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; /* c */ REVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat after a semicolon and a line comment", "DO $$ BEGIN EXECUTE 'SET LOCAL x = 1; -- c\n REVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		{"concat after an E-string newline escape", "DO $$ BEGIN EXECUTE E'SET LOCAL x = 1;\\nREVOKE ALL ON ' || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
		// F2：美元引号的字面量同样会被 || 拼接切开
		{"dollar-quoted function fragment before a concatenation", "DO $$ BEGIN EXECUTE $q$REVOKE ALL ON FUNCTION $q$ || v || ' FROM aegis_app'; END $$;", ""},
		{"dollar-quoted fragment cut before the object kind", "DO $$ BEGIN EXECUTE $q$REVOKE ALL ON $q$ || 'FUNCTION app.x(uuid) FROM aegis_app'; END $$;", ""},
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
		// 表、序列、模式的语句带 % 不是函数类，不管
		{"format template on a table stays ignored", "DO $$ BEGIN EXECUTE format('REVOKE ALL ON TABLE %I FROM aegis_app', t); END $$;", "", nil},
		{"concat on a table stays ignored", "DO $$ BEGIN EXECUTE 'GRANT SELECT ON TABLE ' || quote_ident(t) || ' TO aegis_app'; END $$;", "", nil},
		{"DROP after another statement on the same line", "REVOKE ALL ON FUNCTION app.gone(uuid) FROM aegis_app;", "SELECT 1; DROP FUNCTION app.gone(uuid);\n", nil},
		// I2：拼接判红只在 GRANT/REVOKE 是字面量第一个词时；文案里的 grant、revoke 后面跟 || 不是权限语句
		{"message ending in grant before a concatenation", "DO $$ BEGIN RAISE NOTICE '%', 'traffic pack grant ' || v_id; END $$;", "", nil},
		{"message ending in revoke before a concatenation", "DO $$ BEGIN RAISE EXCEPTION 'cannot revoke' || v_reason; END $$;", "", nil},
		// G1 放宽后仍不算：分号之后的第一个词不是 GRANT/REVOKE
		{"message with a semicolon then text mentioning revoke", "DO $$ BEGIN RAISE NOTICE '%', 'step one; then revoke ' || v_id; END $$;", "", nil},
		{"message with a semicolon then text mentioning grant", "DO $$ BEGIN RAISE NOTICE '%', 'done; see grant ' || v_id; END $$;", "", nil},
		// F1 放宽后仍不算：注释、转义之外还有别的词；美元引号文案里的 grant 不在开头
		{"message with a comment then text mentioning revoke", "DO $$ BEGIN RAISE NOTICE '%', '/* note */ please revoke ' || v_id; END $$;", "", nil},
		{"E-string message with an escape then text mentioning revoke", "DO $$ BEGIN RAISE NOTICE '%', E'line one\\nthen revoke ' || v_id; END $$;", "", nil},
		{"dollar-quoted message mentioning grant", "DO $$ BEGIN RAISE NOTICE '%', $m$please grant $m$ || v_id; END $$;", "", nil},
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
	// GRANT/REVOKE 关键字（第 1 组）：词边界，或紧跟在 E 串的转义 \n \t \r \b \f 之后（E'…;\nREVOKE …'，F1）
	privilegeStart = regexp.MustCompile(`(?i)(?:\b|\\[ntrbf])(REVOKE|GRANT)\b`)
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
	// 美元引号的定界符：$$ 或 $tag$（tag 不以数字开头，否则是位置参数 $1）
	dollarTag = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)
	// 语句被 '…' || '…' 拼接截断：结尾的单引号或美元引号（$q$ …$q$，F2）后面跟 ||
	concatAfter = regexp.MustCompile(`^(?:'|\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$)\s*\|\|`)
	// 文本以美元引号的定界符结尾（$$ 或 $tag$）：权限语句是美元引号字面量的第一个词
	endsWithDollarTag = regexp.MustCompile(`\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$$`)
	// 已经写出了「ON 对象」；没写出来说明语句在 ON 之前或之后被截断了
	onObject = regexp.MustCompile(`(?is)\bON\s+\S`)
	// DROP 豁免只认语句开头：前一个非空白字符是分号或文本开头

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

// stripSQLComments 去掉 -- 行注释和（可嵌套的）/* */ 块注释，单引号字符串（含 E'…' 的反斜杠转义）与双引号标识符里的
// 原样保留（EXECUTE '…' 里的静态语句仍要扫）。美元引号 $tag$…$tag$ 的结尾按原文定位，里面既可能是字符串
// （其中的 -- 不是注释），也可能是 DO 函数体（其中的注释要去掉），所以只在定界符之内递归去注释，定界符本身保留。
// 行注释留下换行，块注释换成一个空格
func stripSQLComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'' || c == '"':
			// E'…' 里反斜杠转义下一个字符；E 前面不能是标识符字符（否则只是名字的结尾，例如 name'）
			escape := c == '\'' && i > 0 && (s[i-1] == 'E' || s[i-1] == 'e') && (i < 2 || !isIdentByte(s[i-2]))
			j := i + 1
			for j < len(s) {
				if escape && s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c { // 连写两个引号是转义
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j > len(s) {
				j = len(s)
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			i = j
		case c == '$' && (i == 0 || !isIdentByte(s[i-1])) && dollarTag.MatchString(s[i:]):
			tag := dollarTag.FindString(s[i:])
			bodyStart := i + len(tag)
			k := strings.Index(s[bodyStart:], tag)
			if k < 0 { // 没有结尾：剩下的当作一整段正文
				b.WriteString(tag)
				b.WriteString(stripSQLComments(s[bodyStart:]))
				i = len(s)
				break
			}
			b.WriteString(tag)
			b.WriteString(stripSQLComments(s[bodyStart : bodyStart+k]))
			b.WriteString(tag)
			i = bodyStart + k + len(tag)
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

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// statementEnd 返回从 from 起这条语句的结尾：分号、单引号（字面量结尾）、美元引号或文本尾
func statementEnd(s string, from int) int {
	if k := strings.IndexAny(s[from:], ";'$"); k >= 0 {
		return from + k
	}
	return len(s)
}

// insideSingleQuoted 看 pos 是不是落在单引号字面量里：'…'（” 是转义的引号）、E'…'（另认 \' 转义）。
// 美元引号的内容是代码（DO 块的函数体），不算字面量
func insideSingleQuoted(text string, pos int) bool {
	_, _, in := singleQuotedOpen(text, pos)
	return in
}

// singleQuotedOpen 同 insideSingleQuoted，另返回 pos 所在字面量的开引号位置，以及它是不是 E 串（认反斜杠转义）
func singleQuotedOpen(text string, pos int) (open int, escapes, in bool) {
	open = -1
	for i := 0; i < pos && i < len(text); i++ {
		c := text[i]
		if !in {
			if c == '\'' {
				in, open = true, i
				escapes = i > 0 && (text[i-1] == 'E' || text[i-1] == 'e') && (i < 2 || !isIdentByte(text[i-2]))
			}
			continue
		}
		switch {
		case escapes && c == '\\':
			i++
		case c == '\'' && i+1 < len(text) && text[i+1] == '\'':
			i++
		case c == '\'':
			in = false
		}
	}
	return open, escapes, in
}

// startsStatementInLiteral 看 pos 是不是字面量里某条语句的第一个词（拼接判红用）：
//   - 单引号字面量：从开引号到 pos 的正文先还原转义（” 还原成 '；E 串里的 \n、\t 等当空白），再去掉注释
//     （/* */、-- 到行尾），取最后一个分号之后的部分，剩下全是空白才算（G1、F1：'SET LOCAL x = 1; /* c */ REVOKE …'）；
//   - 美元引号字面量（$q$…$q$，F2）：pos 前面紧挨着（隔空白）它的开定界符。
//
// 'cannot revoke'、'step one; then revoke'、'/* note */ please revoke' 这类文案里，grant/revoke 前面还有别的词，不算
func startsStatementInLiteral(text string, pos int) bool {
	open, escapes, in := singleQuotedOpen(text, pos)
	if !in {
		return endsWithDollarTag.MatchString(strings.TrimRightFunc(text[:pos], unicode.IsSpace))
	}
	body := stripSQLComments(unescapeLiteral(text[open+1:pos], escapes))
	if i := strings.LastIndexByte(body, ';'); i >= 0 {
		body = body[i+1:]
	}
	return strings.TrimSpace(body) == ""
}

// unescapeLiteral 还原单引号字面量正文：” → '；E 串里 \n \t \r \b \f 变成空白，其余 \x 变成 x
func unescapeLiteral(body string, escapes bool) string {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '\'' && i+1 < len(body) && body[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case escapes && c == '\\' && i+1 < len(body):
			i++
			switch body[i] {
			case 'n', 't', 'r', 'b', 'f':
				b.WriteByte(' ')
			default:
				b.WriteByte(body[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// atStatementStart 看 pos 之前第一个非空白字符是不是分号（或已到文本开头）
func atStatementStart(text string, pos int) bool {
	k := strings.LastIndexFunc(text[:pos], func(r rune) bool { return !unicode.IsSpace(r) })
	return k < 0 || text[k] == ';'
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
// 函数类语句带 %（format() 模板）、或被 '…' || '…' 拼接截断，是动态 SQL，静态扫不了，同样报错；
// 表、序列、模式的语句不管。DROP 豁免只认出现在语句开头的 DROP FUNCTION / PROCEDURE / ROUTINE
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
			loc := privilegeStart.FindStringSubmatchIndex(text[from:])
			if loc == nil {
				break
			}
			start, matchEnd := from+loc[2], from+loc[3]
			end := statementEnd(text, matchEnd)
			from = end
			stmt := strings.Join(strings.Fields(text[start:end]), " ")
			// 拼接：'REVOKE ALL ON FUNCTION ' || 'app.x(uuid) FROM aegis_app' 被切成碎片，每片单看都不像完整语句。
			// 碎片是函数类、或还没写出 ON 对象（不知道是什么对象）时判红；明确是表、序列的拼接不管
			// 已写出函数类对象的碎片一律判红；还没写出 ON 对象的碎片，只在 GRANT/REVOKE 是字面量里某条语句的
			// 第一个词时才算拼接出来的权限语句（'cannot revoke' || … 这类文案不算）
			if end < len(text) && concatAfter.MatchString(text[end:]) &&
				(functionObjectKind.MatchString(stmt) || (!onObject.MatchString(stmt) && startsStatementInLiteral(text, start))) {
				problems = append(problems, fmt.Sprintf("%s: privilege statement built by string concatenation cannot be checked: %s", m.name, stmt))
				continue
			}
			if !functionObjectKind.MatchString(stmt) {
				continue
			}
			// format() 模板（含 %）是动态 SQL，静态扫不了：函数类的一律判红，不看角色（角色可能经 %I 传进来）
			if strings.Contains(stmt, "%") {
				problems = append(problems, fmt.Sprintf("%s: function privilege statement with a format() template cannot be checked: %s", m.name, stmt))
				continue
			}
			if !mentionsRole(stmt, "aegis_app") {
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
			// ALTER EXTENSION … DROP FUNCTION、字符串里的 DROP 文本（含 'x; DROP FUNCTION …'）都不算
			if !atStatementStart(text, start) || insideSingleQuoted(text, start) {
				from = matchEnd
				continue
			}
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
