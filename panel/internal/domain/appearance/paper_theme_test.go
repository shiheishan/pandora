// [INPUT]: 依赖 tokens.go 的 DesignTokenKeys / normalizeTokens / filterTokens，依赖 service.go 的 SaveTheme 入参校验，读取 migrations/00075 与前端 design-tokens.ts
// [OUTPUT]: 对外提供「默认 · 纸白」迁移、token 白名单与主题保存校验的单元测试
// [POS]: domain/appearance 的契约测试：前端 design-tokens.ts 是令牌名单与取值的唯一来源，Go 白名单与迁移种子都必须与它逐条一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package appearance

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type designToken struct{ name, light, dark string }

// frontendColorTokens 从前端 design-tokens.ts 的 COLOR_TOKENS 里逐行取出 43 个令牌。
func frontendColorTokens(t *testing.T) []designToken {
	t.Helper()
	src := readFile(t, filepath.Join("..", "..", "..", "frontend", "src", "styles", "design-tokens.ts"))
	re := regexp.MustCompile(`(?m)^\s*c\('\w+', '(--[a-z0-9-]+)', '([^']*)', '([^']*)', '[^']*'\),`)
	var out []designToken
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, designToken{m[1], m[2], m[3]})
	}
	if len(out) != 43 {
		t.Fatalf("design-tokens.ts 解析出 %d 个颜色令牌，应为 43（格式变了就改这里的解析）", len(out))
	}
	return out
}

func TestDesignTokenKeysMatchFrontend(t *testing.T) {
	var names []string
	for _, tok := range frontendColorTokens(t) {
		names = append(names, tok.name)
	}
	if !reflect.DeepEqual(names, DesignTokenKeys) {
		t.Fatalf("DesignTokenKeys 与 design-tokens.ts 不一致\nGo:  %v\nTS:  %v", DesignTokenKeys, names)
	}
}

// 「默认 · 纸白」的两组取值必须与 design-tokens.ts 逐字一致，站点名是 Pandora。
func TestPaperThemeSeedMatchesFrontend(t *testing.T) {
	sqlText := readFile(t, filepath.Join(migrationsDir(t), "00075_theme_paper_white.sql"))
	up := sqlText[strings.Index(sqlText, "+goose Up"):strings.Index(sqlText, "+goose Down")]

	start := strings.Index(up, "'{\n    \"light\"")
	end := strings.Index(up, "}'::jsonb")
	if start < 0 || end < 0 {
		t.Fatal("找不到 paper 的 tokens 字面量")
	}
	var seeded map[string]map[string]string
	if err := json.Unmarshal([]byte(up[start+1:end+1]), &seeded); err != nil {
		t.Fatalf("paper tokens 不是合法 JSON：%v", err)
	}
	want := map[string]map[string]string{"light": {}, "dark": {}}
	for _, tok := range frontendColorTokens(t) {
		want["light"][tok.name] = tok.light
		want["dark"][tok.name] = tok.dark
	}
	if !reflect.DeepEqual(seeded, want) {
		t.Fatalf("paper tokens 与 design-tokens.ts 不一致\n种子：%v\n前端：%v", seeded, want)
	}
	if _, err := normalizeTokens(json.RawMessage(up[start+1 : end+1])); err != nil {
		t.Fatalf("种子本身过不了保存校验：%v", err)
	}
	for _, need := range []string{
		`'paper', '默认 · 纸白', true, true`,
		`'{"site_name":"Pandora"}'::jsonb`,
		"WHERE is_builtin\n   AND code IN ('default', 'midnight', 'aurora', 'stellar', 'stellar-dark', 'stellar-light')",
	} {
		if !strings.Contains(up, need) {
			t.Fatalf("Up 段缺少：%s", need)
		}
	}
}

// Down 要把六个旧内置主题原样补回，并在无主题生效时恢复 default。
func TestPaperThemeDownRestoresOldBuiltins(t *testing.T) {
	sqlText := readFile(t, filepath.Join(migrationsDir(t), "00075_theme_paper_white.sql"))
	down := sqlText[strings.Index(sqlText, "+goose Down"):]
	old := readFile(t, filepath.Join(migrationsDir(t), "00051_theme_slots_hooks.sql")) +
		readFile(t, filepath.Join(migrationsDir(t), "00055_theme_stellar.sql"))
	for _, code := range []string{"default", "midnight", "aurora", "stellar", "stellar-dark", "stellar-light"} {
		was, now := themeBlock(old, code), themeBlock(down, code)
		if now == "" {
			t.Fatalf("Down 没有补回 %s", code)
		}
		// 比 tokens 与 branding 的字面量：Down 必须恢复原值，而不是另写一套
		if norm(was) != norm(now) {
			t.Errorf("%s 的 Down 取值与原迁移不同\n原：%s\n回：%s", code, was, now)
		}
	}
	if !strings.Contains(down, "DELETE FROM site_themes WHERE is_builtin AND code = 'paper'") ||
		!strings.Contains(down, "s.code = 'default'") || !strings.Contains(down, "NOT EXISTS") {
		t.Fatal("Down 必须删 paper，并在没有生效主题时让 default 生效")
	}
}

// norm 只留下 JSON 字面量部分，忽略注释与缩进的差别。
func norm(block string) string {
	var parts []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--") || line == "" {
			continue
		}
		parts = append(parts, line)
	}
	joined := strings.Join(parts, "")
	if i := strings.Index(joined, "'{"); i >= 0 {
		joined = joined[i:]
	}
	if i := strings.LastIndex(joined, "}'"); i >= 0 {
		joined = joined[:i+2]
	}
	return joined
}

func TestNormalizeTokensRejectsOffWhitelist(t *testing.T) {
	ok := `{"light":{"--brand":"#b9442b"},"dark":{"--brand":"#e46e52","--sidebar":"#0a0a0b"}}`
	if _, err := normalizeTokens(json.RawMessage(ok)); err != nil {
		t.Fatalf("合法 tokens 被拒：%v", err)
	}
	for name, raw := range map[string]string{
		"旧扁平键":  `{"brand":"#fff"}`,
		"未知分组":  `{"sepia":{"--bg":"#fff"}}`,
		"不带前缀":  `{"light":{"bg":"#fff"}}`,
		"白名单外":  `{"light":{"--radius-lg":"12px"}}`,
		"分号":    `{"light":{"--bg":"#fff;color:red"}}`,
		"花括号":   `{"dark":{"--bg":"}body{"}}`,
		"非字符串":  `{"light":{"--bg":1}}`,
		"空值":    `{"light":{"--bg":"  "}}`,
		"数组":    `[]`,
		"组不是对象": `{"light":"#fff"}`,
	} {
		if _, err := normalizeTokens(json.RawMessage(raw)); !isValidationOn(err, "tokens") {
			t.Errorf("%s 应被拒（422 tokens），得到 %v", name, err)
		}
	}
}

func TestFilterTokensKeepsOnlyWhitelist(t *testing.T) {
	raw := `{"brand":"#6d5efc","light":{"--bg":"#fff","bg":"#000","--x":"1","--brand":"a;b"},"dark":{"--text":"#eee"},"extra":{"--bg":"#111"}}`
	var got map[string]map[string]string
	if err := json.Unmarshal(filterTokens(json.RawMessage(raw)), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{"light": {"--bg": "#fff"}, "dark": {"--text": "#eee"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterTokens = %v, want %v", got, want)
	}
	// 旧主题整组扁平键：门户拿到的是空对象，不是旧变量名
	if string(filterTokens(json.RawMessage(`{"brand":"#6d5efc","r-lg":"14px"}`))) != `{}` {
		t.Fatal("旧扁平 tokens 应被整体滤掉")
	}
}

// 缺陷 20：违反表上 CHECK 的输入在进库之前就回 422，带字段名。
// 这些输入都走不到事务（Service 没有连接池也不会 panic），证明校验在前。
func TestSaveThemeValidatesBeforeTouchingTheDatabase(t *testing.T) {
	s := &Service{}
	for name, tc := range map[string]struct {
		in    SaveThemeInput
		field string
	}{
		"code 为空":       {SaveThemeInput{Name: "x"}, "code"},
		"code 数字开头":     {SaveThemeInput{Code: "9abc", Name: "x"}, "code"},
		"code 太短":       {SaveThemeInput{Code: "a", Name: "x"}, "code"},
		"code 非法字符":     {SaveThemeInput{Code: "ab cd", Name: "x"}, "code"},
		"name 为空":       {SaveThemeInput{Code: "my-theme", Name: "   "}, "name"},
		"name 超 60 字":   {SaveThemeInput{Code: "my-theme", Name: strings.Repeat("字", 61)}, "name"},
		"custom_css 停用": {SaveThemeInput{Code: "my-theme", Name: "x", CustomCSS: "body{}"}, "custom_css"},
		"tokens 非法":     {SaveThemeInput{Code: "my-theme", Name: "x", Tokens: json.RawMessage(`{"brand":"#fff"}`)}, "tokens"},
		"branding 非对象":  {SaveThemeInput{Code: "my-theme", Name: "x", Branding: json.RawMessage(`"Pandora"`)}, "branding"},
	} {
		if _, err := s.SaveTheme(context.Background(), "t", tc.in); !isValidationOn(err, tc.field) {
			t.Errorf("%s：得到 %v，want 422 fields.%s", name, err, tc.field)
		}
	}
}

func isValidationOn(err error, field string) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == httpx.CodeValidationFailed && he.Fields[field] != ""
}
