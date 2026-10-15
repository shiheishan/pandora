package service

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeBrandingRules(t *testing.T) {
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	got, err := normalizeBranding(json.RawMessage(`{"tagline":"  一路畅通  ","logo":"` + png + `"}`))
	if err != nil {
		t.Fatalf("合法 branding 被拒：%v", err)
	}
	var m map[string]string
	_ = json.Unmarshal(got, &m)
	if !reflect.DeepEqual(m, map[string]string{"tagline": "一路畅通", "logo": png}) {
		t.Fatalf("规范化结果 = %v（标语去空白、带 Logo）", m)
	}

	gotEmpty, err := normalizeBranding(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("空 branding 被拒：%v", err)
	}
	var mEmpty map[string]string
	_ = json.Unmarshal(gotEmpty, &mEmpty)
	if !reflect.DeepEqual(mEmpty, map[string]string{}) {
		t.Fatalf("规范化空对象 = %v，want {}", mEmpty)
	}

	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, MaxLogoBytes+1))
	for name, tc := range map[string]struct{ raw, field string }{
		"带站点名":     {`{"site_name":"星河"}`, "branding.site_name"},
		"标语过长":     {`{"tagline":"` + strings.Repeat("字", 81) + `"}`, "branding.tagline"},
		"Logo 外链":  {`{"logo":"https://example.test/a.png"}`, "branding.logo"},
		"Logo 类型":  {`{"logo":"data:text/html;base64,PGgxPg=="}`, "branding.logo"},
		"Logo 坏数据": {`{"logo":"data:image/png;base64,%%%"}`, "branding.logo"},
		"Logo 过大":  {`{"logo":"` + big + `"}`, "branding.logo"},
		"未知键":      {`{"color":"red"}`, "branding"},
		"不是对象":     {`[]`, "branding"},
	} {
		if _, err := normalizeBranding(json.RawMessage(tc.raw)); !isValidationOn(err, tc.field) {
			t.Errorf("%s：得到 %v，want 422 fields.%s", name, err, tc.field)
		}
	}
}

func TestNormalizeBrandingRejectsNULInTagline(t *testing.T) {
	_, err := normalizeBranding(json.RawMessage(`{"tagline":"a\u0000b"}`))
	if !isValidationOn(err, "branding.tagline") {
		t.Fatalf("NUL in tagline must be rejected on branding.tagline, got %v", err)
	}
}

func TestFilterBrandingKeepsOnlyKnownKeys(t *testing.T) {
	raw := `{"site_name":"潘多拉面板","tagline":"稳定","logo":"https://evil.test/x.png","color":"red","extra":1}`
	var got map[string]string
	if err := json.Unmarshal(filterBranding(json.RawMessage(raw)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"tagline": "稳定"}) {
		t.Fatalf("filterBranding = %v（site_name、外链 Logo 与未知键都不下发）", got)
	}
	if string(filterBranding(json.RawMessage(`null`))) != `{}` {
		t.Fatal("空 branding 应为 {}")
	}
}
