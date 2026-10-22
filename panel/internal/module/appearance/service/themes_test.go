package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
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
	_, err = s.SaveSlot(context.Background(), SaveSlotInput{Key: "portal.footer", Content: "hi"})
	if !thIsValidationOn(err, "key") {
		t.Fatalf("未知 key (portal.footer): %v", err)
	}
	_, err = s.SaveSlot(context.Background(), SaveSlotInput{Key: "portal.plans.notice", Content: "a\x00b"})
	if !thIsValidationOn(err, "content") {
		t.Fatalf("NUL content: %v", err)
	}
}

// 插槽位以最新一次改 CHECK 的迁移为准，再改 CHECK 时把路径换成新迁移。
func TestSlotCatalogMatchesMigration(t *testing.T) {
	path := "../../../../db/migrations/00062_content_appearance_trim.sql"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	upIdx := strings.Index(sql, "-- +goose Up")
	downIdx := strings.Index(sql, "-- +goose Down")
	if upIdx == -1 || downIdx == -1 || upIdx >= downIdx {
		t.Fatalf("无法截出 Up 段: up=%d down=%d", upIdx, downIdx)
	}
	upSQL := sql[upIdx:downIdx]

	prefix := "ADD CONSTRAINT site_slots_slot_key_check CHECK (slot_key IN ("
	start := strings.Index(upSQL, prefix)
	if start == -1 {
		t.Fatalf("Up 段未找到 CHECK 约束起始: %q", prefix)
	}
	endRel := strings.Index(upSQL[start:], "));")
	if endRel == -1 {
		t.Fatalf("未找到 CHECK 约束结束 ));")
	}
	checkSub := upSQL[start : start+endRel]

	re := regexp.MustCompile(`'([a-z.]+)'`)
	matches := re.FindAllStringSubmatch(checkSub, -1)
	if len(matches) == 0 {
		t.Fatalf("未正则匹配到插槽 key: %s", checkSub)
	}

	migKeys := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		migKeys[m[1]] = struct{}{}
	}

	catalogKeys := make(map[string]struct{}, len(SlotCatalog))
	for _, c := range SlotCatalog {
		catalogKeys[c.Key] = struct{}{}
	}

	if len(migKeys) != len(SlotCatalog) || len(catalogKeys) != len(SlotCatalog) {
		t.Fatalf("数量不一致: 迁移中 %d 个 key, SlotCatalog 中 %d 个 key (去重 %d)", len(migKeys), len(SlotCatalog), len(catalogKeys))
	}
	for k := range migKeys {
		if _, ok := catalogKeys[k]; !ok {
			t.Fatalf("SlotCatalog 缺少迁移中定义的 key: %s", k)
		}
	}
	for k := range catalogKeys {
		if _, ok := migKeys[k]; !ok {
			t.Fatalf("迁移中缺少 SlotCatalog 定义的 key: %s", k)
		}
	}
}
