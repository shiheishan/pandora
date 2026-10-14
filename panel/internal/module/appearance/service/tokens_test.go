package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type designToken struct{ name, light, dark string }

// frontendColorTokens 从前端 design-tokens.ts 的 COLOR_TOKENS 里逐行取出 43 个令牌。
func frontendColorTokens(t *testing.T) []designToken {
	t.Helper()
	src := readFile(t, filepath.Join("..", "..", "..", "..", "frontend", "src", "styles", "design-tokens.ts"))
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
	sqlText := readFile(t, filepath.Join("..", "..", "..", "..", "db", "migrations", "00029_appearance.sql"))
	up := sqlText[strings.Index(sqlText, "+goose Up"):strings.Index(sqlText, "+goose Down")]

	start := strings.Index(up, "'{\n    \"light\"")
	if start < 0 {
		t.Fatal("找不到 paper 的 tokens 字面量")
	}
	// 终点从起点之后找：建表语句里的 DEFAULT '{}'::jsonb 也会匹配
	end := strings.Index(up[start:], "}'::jsonb")
	if end < 0 {
		t.Fatal("找不到 paper 的 tokens 字面量的结尾")
	}
	end += start
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
	} {
		if !strings.Contains(up, need) {
			t.Fatalf("Up 段缺少：%s", need)
		}
	}
}

// 旧前端 applySiteTheme 里的两道校验，原样复制过来（历史种子按它写）。
// 新门户的白名单是 DesignTokenKeys，见本文件。
var (
	themeTokenKeyRe = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	themeTokenBadRe = regexp.MustCompile(`[;{}<>]`)
)

// 内置主题的每个 token 都必须能通过前端校验。
func TestBuiltinThemeTokensPassFrontendValidation(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "db", "migrations", "00029_appearance.sql")
	sqlText := readFile(t, path)
	for _, raw := range extractJSONObjects(sqlText) {
		var tokens map[string]any
		if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
			continue
		}
		if _, isBranding := tokens["site_name"]; isBranding {
			continue
		}
		if _, isBranding := tokens["tagline"]; isBranding {
			continue
		}
		flat := map[string]any{}
		for k, v := range tokens {
			if group, ok := v.(map[string]any); ok && (k == "light" || k == "dark") {
				for gk, gv := range group {
					flat[gk] = gv
				}
				continue
			}
			flat[k] = v
		}
		for k, v := range flat {
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
		if _, err := normalizeTokens(json.RawMessage(raw)); !isValidationUnder(err, "tokens") {
			t.Errorf("%s 应被拒（422 tokens 或 tokens.<组>.<键>），得到 %v", name, err)
		}
	}
}

func TestNormalizeTokensMarksEachBadValue(t *testing.T) {
	_, err := normalizeTokens(json.RawMessage(`{"light":{"--bg":"#fff;x","--brand":"#b9442b"},"dark":{"--text":" "}}`))
	var he *errs.Error
	if !errors.As(err, &he) || len(he.Fields) != 2 ||
		he.Fields["tokens.light.--bg"] == "" || he.Fields["tokens.dark.--text"] == "" {
		t.Fatalf("want fields tokens.light.--bg and tokens.dark.--text, got %v", err)
	}
	if _, err := normalizeTokens(json.RawMessage(`{"light":{"--nope":"#fff"}}`)); !isValidationOn(err, "tokens") {
		t.Fatalf("unknown key must be marked on tokens, got %v", err)
	}
}

func TestNormalizeTokensRejectsNULInValue(t *testing.T) {
	_, err := normalizeTokens(json.RawMessage(`{"light":{"--bg":"#fff\u0000"}}`))
	if !isValidationOn(err, "tokens.light.--bg") {
		t.Fatalf("NUL in value must be rejected on tokens.light.--bg, got %v", err)
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
	if string(filterTokens(json.RawMessage(`{"brand":"#6d5efc","r-lg":"14px"}`))) != `{}` {
		t.Fatal("旧扁平 tokens 应被整体滤掉")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不了 %s：%v", path, err)
	}
	return string(b)
}

// extractJSONObjects 把 SQL 里被单引号包着、跨行拼接的 JSON 还原出来。
func extractJSONObjects(sqlText string) []string {
	var out []string
	for _, chunk := range strings.Split(sqlText, "'{") {
		end := strings.Index(chunk, "}'")
		if end < 0 {
			continue
		}
		body := chunk[:end]
		body = strings.ReplaceAll(body, "'\n", "")
		body = strings.ReplaceAll(body, "'", "")
		out = append(out, "{"+strings.TrimSpace(body)+"}")
	}
	return out
}

func isValidationUnder(err error, prefix string) bool {
	var he *errs.Error
	if !errors.As(err, &he) || he.Code != errs.ValidationFailed {
		return false
	}
	for k := range he.Fields {
		if k == prefix || strings.HasPrefix(k, prefix+".") {
			return true
		}
	}
	return false
}

func isValidationOn(err error, field string) bool {
	var he *errs.Error
	return errors.As(err, &he) && he.Code == errs.ValidationFailed && he.Fields[field] != ""
}
