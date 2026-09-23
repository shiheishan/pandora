package appearance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 前端 applySiteTheme 里的两道校验，原样复制过来。
//
// 复制而不是引用，是因为那段逻辑住在 HTML 里的 <script> 中，Go 这边引不到。
// 两处必须保持一致——不一致的后果是内置主题里的某个 token 在前端被静默
// 丢弃，管理员看到主题「启用成功」，页面上却少了一半效果，而且没有任何报错。
var (
	themeTokenKeyRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	themeTokenBadRe = regexp.MustCompile(`[;{}<>]`)
)

// 内置主题的每个 token 都必须能通过前端校验。
//
// 这条守的是「写进迁移的值到底能不能生效」。前端对键名和取值都有过滤，
// 一个带分号的取值会被整条跳过——不是报错，是当它不存在。
func TestBuiltinThemeTokensPassFrontendValidation(t *testing.T) {
	for _, path := range themeMigrations(t) {
		sqlText := readFile(t, path)
		for _, raw := range extractJSONObjects(sqlText) {
			var tokens map[string]any
			if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
				continue // 解析不了的不是我们要检的东西
			}
			// branding 和 tokens 在 SQL 里长得一样，都是单引号包的 JSON。
			// branding 存的是站名、标语这类文本，不走 CSS 变量那条路，
			// 键名自然也不必满足 CSS 变量的命名规则。
			if _, isBranding := tokens["site_name"]; isBranding {
				continue
			}
			if _, isBranding := tokens["tagline"]; isBranding {
				continue
			}
			for k, v := range tokens {
				s, ok := v.(string)
				if !ok {
					t.Errorf("%s: token %q 不是字符串（前端只接受字符串）", filepath.Base(path), k)
					continue
				}
				if !themeTokenKeyRe.MatchString(k) {
					t.Errorf("%s: token 键名 %q 过不了前端的 %s", filepath.Base(path), k, themeTokenKeyRe)
				}
				if themeTokenBadRe.MatchString(s) {
					t.Errorf("%s: token %q 的取值含 ; { } < > 之一，会被前端整条丢弃：%q",
						filepath.Base(path), k, s)
				}
			}
		}
	}
}

// Stellar 那套要把底色也换掉，不能只有品牌色——那样换主题的感知太弱，
// 和前三套没区别。
func TestStellarThemeOverridesSurfaceColors(t *testing.T) {
	sqlText := readFile(t, filepath.Join(migrationsDir(t), "00055_theme_stellar.sql"))
	for _, want := range []string{"stellar", "stellar-dark", "stellar-light"} {
		if !strings.Contains(sqlText, "'"+want+"'") {
			t.Errorf("缺少主题 %q", want)
		}
	}
	// 深浅两个变体必须覆盖底色层级，否则深空主题只是「默认深色 + 蓝按钮」
	for _, code := range []string{"stellar-dark", "stellar-light"} {
		block := themeBlock(sqlText, code)
		if block == "" {
			t.Fatalf("找不到 %s 的定义块", code)
		}
		for _, key := range []string{"bg", "surface", "surface-2", "fg", "line"} {
			if !strings.Contains(block, `"`+key+`"`) {
				t.Errorf("%s 没有覆盖 %q，底色不会变", code, key)
			}
		}
	}
	// 中性变体刻意不碰底色：它要跟随用户自己选的深浅色
	if block := themeBlock(sqlText, "stellar"); strings.Contains(block, `"bg"`) {
		t.Error("中性的 stellar 不该覆盖 bg——它的定位是只换品牌色")
	}
}

// 迁移不能把正在生效的主题删掉。回滚时站点当场变回默认配色，
// 而管理员不会知道是谁动的。
func TestThemeMigrationDownDoesNotDropActiveTheme(t *testing.T) {
	sqlText := readFile(t, filepath.Join(migrationsDir(t), "00055_theme_stellar.sql"))
	down := sqlText[strings.Index(sqlText, "+goose Down"):]
	if !strings.Contains(down, "NOT is_active") {
		t.Error("Down 段没有排除正在生效的主题")
	}
}

//------------------------------------------------------------------------------

func migrationsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "migrations")
}

func themeMigrations(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(migrationsDir(t), "*theme*.sql"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("找不到主题迁移：%v", err)
	}
	return matches
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不了 %s：%v", path, err)
	}
	return string(b)
}

// themeBlock 截出某个主题 code 之后、到下一个主题或结尾之间的文本。
func themeBlock(sqlText, code string) string {
	i := strings.Index(sqlText, "('"+code+"',")
	if i < 0 {
		return ""
	}
	rest := sqlText[i+len(code):]
	if j := strings.Index(rest, "\n   ('"); j > 0 {
		return rest[:j]
	}
	return rest
}

// extractJSONObjects 把 SQL 里被单引号包着、跨行拼接的 JSON 还原出来。
func extractJSONObjects(sqlText string) []string {
	var out []string
	for _, chunk := range strings.Split(sqlText, "'{") {
		end := strings.Index(chunk, "}'")
		if end < 0 {
			continue
		}
		// 跨行拼接的字面量之间夹着引号和空白，去掉它们才是完整 JSON
		body := chunk[:end]
		body = strings.ReplaceAll(body, "'\n", "")
		body = strings.ReplaceAll(body, "'", "")
		out = append(out, "{"+strings.TrimSpace(body)+"}")
	}
	return out
}
