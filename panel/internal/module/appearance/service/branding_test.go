package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeBrandingRules(t *testing.T) {
	got, err := normalizeBranding(json.RawMessage(`{"tagline":"  一路畅通  "}`))
	if err != nil {
		t.Fatalf("合法 branding 被拒：%v", err)
	}
	var m map[string]string
	_ = json.Unmarshal(got, &m)
	if !reflect.DeepEqual(m, map[string]string{"tagline": "一路畅通"}) {
		t.Fatalf("规范化结果 = %v（标语去空白）", m)
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

	for name, tc := range map[string]struct{ raw, field string }{
		"带站点名":    {`{"site_name":"星河"}`, "branding.site_name"},
		"标语过长":    {`{"tagline":"` + strings.Repeat("字", 81) + `"}`, "branding.tagline"},
		"Logo 已删": {`{"logo":"data:image/png;base64,AAAA"}`, "branding"},
		"未知键":     {`{"color":"red"}`, "branding"},
		"不是对象":    {`[]`, "branding"},
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
	raw := `{"site_name":"潘多拉面板","tagline":"稳定","logo":"data:image/png;base64,AAAA","color":"red","extra":1}`
	var got map[string]string
	if err := json.Unmarshal(filterBranding(json.RawMessage(raw)), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]string{"tagline": "稳定"}) {
		t.Fatalf("filterBranding = %v（site_name、logo 与未知键都不下发）", got)
	}
	if string(filterBranding(json.RawMessage(`null`))) != `{}` {
		t.Fatal("空 branding 应为 {}")
	}
}
