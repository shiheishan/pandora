// [INPUT]: 依赖 branding.go 的 normalizeBranding / filterBranding / MaxLogoBytes，依赖 service.go 的 SaveTheme 入参校验，依赖 paper_theme_test.go 的 isValidationOn
// [OUTPUT]: 对外提供 TestNormalizeBrandingRules、TestFilterBrandingKeepsOnlyKnownKeys、TestSaveThemeReportsAllFieldsAtOnce
// [POS]: domain/appearance 站点品牌规则的单元测试：站点名必填与长度、标语长度、Logo 只收小体积 data:image、未知键拒绝；门户读取只放行三个已知键；保存一次回齐全部字段错误

package appearance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestNormalizeBrandingRules(t *testing.T) {
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	got, err := normalizeBranding(json.RawMessage(`{"site_name":"  星河  ","tagline":"","logo":"` + png + `"}`))
	if err != nil {
		t.Fatalf("合法 branding 被拒：%v", err)
	}
	var m map[string]string
	_ = json.Unmarshal(got, &m)
	if !reflect.DeepEqual(m, map[string]string{"site_name": "星河", "logo": png}) {
		t.Fatalf("规范化结果 = %v（站点名去空白、空标语不存）", m)
	}

	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxLogoBytes+1))
	for name, tc := range map[string]struct{ raw, field string }{
		"站点名缺失":    {`{}`, "branding.site_name"},
		"站点名空白":    {`{"site_name":"   "}`, "branding.site_name"},
		"站点名过长":    {`{"site_name":"` + strings.Repeat("字", 41) + `"}`, "branding.site_name"},
		"站点名非字符串":  {`{"site_name":1}`, "branding.site_name"},
		"标语过长":     {`{"site_name":"x","tagline":"` + strings.Repeat("字", 81) + `"}`, "branding.tagline"},
		"Logo 外链":  {`{"site_name":"x","logo":"https://example.test/a.png"}`, "branding.logo"},
		"Logo 类型":  {`{"site_name":"x","logo":"data:text/html;base64,PGgxPg=="}`, "branding.logo"},
		"Logo 坏数据": {`{"site_name":"x","logo":"data:image/png;base64,%%%"}`, "branding.logo"},
		"Logo 过大":  {`{"site_name":"x","logo":"` + big + `"}`, "branding.logo"},
		"未知键":      {`{"site_name":"x","color":"red"}`, "branding"},
		"不是对象":     {`[]`, "branding"},
	} {
		if _, err := normalizeBranding(json.RawMessage(tc.raw)); !isValidationOn(err, tc.field) {
			t.Errorf("%s：得到 %v，want 422 fields.%s", name, err, tc.field)
		}
	}
}

func TestFilterBrandingKeepsOnlyKnownKeys(t *testing.T) {
	raw := `{"site_name":"潘多拉面板","tagline":"稳定","logo":"https://evil.test/x.png","color":"red","extra":1}`
	var got map[string]string
	if err := json.Unmarshal(filterBranding(json.RawMessage(raw)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"site_name": "潘多拉面板", "tagline": "稳定"}) {
		t.Fatalf("filterBranding = %v（外链 Logo 与未知键都不下发）", got)
	}
	if string(filterBranding(json.RawMessage(`null`))) != `{}` {
		t.Fatal("空 branding 应为 {}")
	}
}

// 名称、令牌、品牌的错误合进同一个 422，编辑器一次标齐（不走到事务）。
func TestSaveThemeReportsAllFieldsAtOnce(t *testing.T) {
	_, err := (&Service{}).SaveTheme(context.Background(), "t", SaveThemeInput{
		Code: "ok-code", Name: "", Create: true,
		Tokens:   json.RawMessage(`{"light":{"--bg":"a{b"}}`),
		Branding: json.RawMessage(`{"site_name":""}`),
	})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
		t.Fatalf("want 422, got %v", err)
	}
	for _, f := range []string{"name", "tokens.light.--bg", "branding.site_name"} {
		if he.Fields[f] == "" {
			t.Errorf("fields 缺 %s：%v", f, he.Fields)
		}
	}
}
