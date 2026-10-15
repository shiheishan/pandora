package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func thIsValidationOn(err error, field string) bool {
	var he *errs.Error
	return errors.As(err, &he) && he.Code == errs.ValidationFailed && he.Fields[field] != ""
}

func thIsValidationUnder(err error, prefix string) bool {
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

func TestSaveThemeValidatesBeforeTouchingTheDatabase(t *testing.T) {
	s := &Service{}
	for name, tc := range map[string]struct {
		in    SaveThemeInput
		field string
	}{
		"code 为空":      {SaveThemeInput{Name: "x"}, "code"},
		"code 数字开头":    {SaveThemeInput{Code: "9abc", Name: "x"}, "code"},
		"code 太短":      {SaveThemeInput{Code: "a", Name: "x"}, "code"},
		"code 非法字符":    {SaveThemeInput{Code: "ab cd", Name: "x"}, "code"},
		"name 为空":      {SaveThemeInput{Code: "my-theme", Name: "   "}, "name"},
		"name 超 60 字":  {SaveThemeInput{Code: "my-theme", Name: strings.Repeat("字", 61)}, "name"},
		"tokens 非法":    {SaveThemeInput{Code: "my-theme", Name: "x", Tokens: json.RawMessage(`{"brand":"#fff"}`)}, "tokens"},
		"branding 非对象": {SaveThemeInput{Code: "my-theme", Name: "x", Branding: json.RawMessage(`"Pandora"`)}, "branding"},
		"带站点名":         {SaveThemeInput{Code: "my-theme", Name: "x", Branding: json.RawMessage(`{"site_name":"x"}`)}, "branding.site_name"},
	} {
		if err := s.SaveTheme(context.Background(), tc.in); !thIsValidationOn(err, tc.field) && !thIsValidationUnder(err, tc.field) {
			t.Errorf("%s：得到 %v，want 422 fields.%s", name, err, tc.field)
		}
	}
}

func TestSaveThemeReportsAllFieldsAtOnce(t *testing.T) {
	err := (&Service{}).SaveTheme(context.Background(), SaveThemeInput{
		Code: "ok-code", Name: "", Create: true,
		Tokens:   json.RawMessage(`{"light":{"--bg":"a{b"}}`),
		Branding: json.RawMessage(`{"tagline":"` + strings.Repeat("字", 81) + `"}`),
	})
	var he *errs.Error
	if !errors.As(err, &he) || he.Code != errs.ValidationFailed {
		t.Fatalf("want 422, got %v", err)
	}
	for _, f := range []string{"name", "tokens.light.--bg", "branding.tagline"} {
		if he.Fields[f] == "" {
			t.Errorf("fields 缺 %s：%v", f, he.Fields)
		}
	}
}

func TestSaveThemeRejectsNameNUL(t *testing.T) {
	err := (&Service{}).SaveTheme(context.Background(), SaveThemeInput{
		Code: "my-theme", Name: "a\x00b",
		Tokens:   json.RawMessage(`{"light":{"--bg":"#fff"},"dark":{"--bg":"#000"}}`),
		Branding: json.RawMessage(`{}`),
	})
	if !thIsValidationOn(err, "name") {
		t.Fatalf("name NUL: %v", err)
	}
}

func TestSaveSlotRejectsUnknownKeyAndNUL(t *testing.T) {
	s := &Service{}
	_, err := s.SaveSlot(context.Background(), SaveSlotInput{Key: "unknown.slot", Content: "hi"})
	if !thIsValidationOn(err, "key") {
		t.Fatalf("未知 key: %v", err)
	}
	_, err = s.SaveSlot(context.Background(), SaveSlotInput{Key: "portal.footer", Content: "a\x00b"})
	if !thIsValidationOn(err, "content") {
		t.Fatalf("NUL content: %v", err)
	}
}

func TestSlotCatalogMatchesMigration(t *testing.T) {
	path := "../../../../db/migrations/00029_appearance.sql"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, c := range SlotCatalog {
		needle := "'" + c.Key + "'"
		if !strings.Contains(sql, needle) {
			t.Fatalf("迁移里没有插槽 %s（找 %s）", c.Key, needle)
		}
	}
	portalCount := strings.Count(sql, "'portal.")
	if portalCount != len(SlotCatalog) {
		t.Fatalf("迁移里 portal. 出现 %d 次，SlotCatalog 有 %d 项", portalCount, len(SlotCatalog))
	}
}
