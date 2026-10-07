package db

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 连接池归还时不做清理（见 OpenWithOptions），前提是没有任何代码往连接上留会话级状态。
// 这条守卫扫 panel/internal 与 panel/cmd 的全部非测试 Go 源码里的字符串字面量（注释不算）：
//   - set_config 的第三个参数必须是 true（事务级）；唯一例外是把值清成空串的会话级写法
//     （归还钩子的纵深防御清理，只会清掉、不会留下租户）；认不出写法的也算违规；
//   - 不许出现会话级 SET / RESET 业务变量、会话级 advisory 锁；
//   - LISTEN 只许出现在 platform/realtime（它独占一条连接，用完销毁，不还回池里）。
var (
	setConfigCall      = regexp.MustCompile(`set_config\(`)
	setConfigLocalForm = regexp.MustCompile(`set_config\(\s*'app\.[a-z_]+'\s*,\s*(?:(?:\$\d+|'[^']*'|[a-z_.]+(?:::[a-z]+)?)\s*,\s*true|''\s*,\s*false)\s*\)`)
	sessionLevelState  = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bSET\s+(?:SESSION\s+)?app\.`),
		regexp.MustCompile(`(?i)\bRESET\s+(?:ALL|app\.)`),
		regexp.MustCompile(`(?i)\bSET\s+(?:SESSION\s+)?(?:ROLE|search_path|statement_timeout|row_security)\b`),
		regexp.MustCompile(`(?i)\bpg_(?:try_)?advisory_lock(?:_shared)?\(`),
	}
	listenStatement = regexp.MustCompile(`(?i)\bLISTEN\s+[a-z_]`)
)

func TestNoSessionLevelStateOnPooledConnections(t *testing.T) {
	panel := filepath.Join("..", "..", "..")
	realtimeDir := filepath.Join(panel, "internal", "platform", "realtime")
	scanned, recognized := 0, 0
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(panel, root), func(dir string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !hasNonTestGoFile(t, dir) {
				return nil
			}
			src := sqlLiterals(t, sourcetest.Load(t, dir).Source())
			scanned++
			rel, _ := filepath.Rel(panel, dir)
			calls := len(setConfigCall.FindAllStringIndex(src, -1))
			local := len(setConfigLocalForm.FindAllStringIndex(src, -1))
			recognized += local
			if calls != local {
				t.Errorf("%s: %d set_config call(s), only %d are recognizably transaction-local (third argument true)", rel, calls, local)
			}
			for _, re := range sessionLevelState {
				if m := re.FindString(src); m != "" {
					t.Errorf("%s: session-level state %q would leak into the pool", rel, m)
				}
			}
			if dir != realtimeDir {
				if m := listenStatement.FindString(src); m != "" {
					t.Errorf("%s: %q outside platform/realtime would leave a LISTEN on a pooled connection", rel, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// 扫描器坏掉会静默扫出 0 个包
	if scanned < 20 || recognized < 2 {
		t.Fatalf("scanned %d packages and recognized %d set_config calls; the scanner no longer sees the source tree",
			scanned, recognized)
	}
}

// 守卫的正则本身要能认出违规写法，否则上面那条测试永远是绿的。
func TestSessionStateGuardPatterns(t *testing.T) {
	for _, ok := range []string{
		`SELECT set_config('app.tenant_id', $1, true)`,
		`set_config('app.actor_id',  $2, true)`,
		`PERFORM set_config('app.tenant_id', p_tenant::text, true)`,
		`set_config('app.tenant_id', '00000000-0000-7000-8000-000000000001', true)`,
	} {
		if !setConfigLocalForm.MatchString(ok) {
			t.Errorf("transaction-local form not recognized: %s", ok)
		}
	}
	// 清成空串的会话级写法放行：它只会清掉租户，不会留下租户
	if !setConfigLocalForm.MatchString(`set_config('app.tenant_id', '', false)`) {
		t.Error("session-level clearing form not recognized")
	}
	for _, bad := range []string{
		`SELECT set_config('app.tenant_id', $1, false)`,
		`set_config('app.tenant_id', 'x', false)`,
		`set_config('app.tenant_id', $1, $3)`,
	} {
		if setConfigLocalForm.MatchString(bad) {
			t.Errorf("session-level form accepted: %s", bad)
		}
	}
	for _, bad := range []string{
		`SET app.tenant_id = '1'`, `set session app.actor_id to 'x'`, `RESET ALL`,
		`SET ROLE postgres`, `SET search_path TO public`, `SELECT pg_advisory_lock(1)`,
		`SELECT pg_try_advisory_lock_shared(1)`,
	} {
		hit := false
		for _, re := range sessionLevelState {
			hit = hit || re.MatchString(bad)
		}
		if !hit {
			t.Errorf("session-level statement not detected: %s", bad)
		}
	}
	for _, ok := range []string{
		`SET LOCAL app.tenant_id = '1'`, `SET CONSTRAINTS ALL IMMEDIATE`,
		`SELECT pg_advisory_xact_lock($1)`, `SET LOCAL statement_timeout = '5min'`,
	} {
		for _, re := range sessionLevelState {
			if re.MatchString(ok) {
				t.Errorf("transaction-scoped statement flagged by %s: %s", re, ok)
			}
		}
	}
	if !listenStatement.MatchString(`LISTEN aegis_change`) {
		t.Error("LISTEN not detected")
	}
}

// sqlLiterals 取出源码里全部字符串字面量的值：SQL 只会出现在字面量里，注释里
// 提到 set_config 或 LISTEN 不该算数。各文件首尾相接的原文照样能逐词切分。
func sqlLiterals(t *testing.T, src string) string {
	t.Helper()
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("package", fset.Base(), len(src))
	s.Init(file, []byte(src), func(pos token.Position, msg string) {
		t.Fatalf("tokenize package source at %s: %s", pos, msg)
	}, 0)
	var out strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return out.String()
		}
		if tok != token.STRING {
			continue
		}
		v, err := strconv.Unquote(lit)
		if err != nil {
			t.Fatalf("unquote %s: %v", lit, err)
		}
		out.WriteString(v)
		out.WriteString("\n")
	}
}

func hasNonTestGoFile(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}
