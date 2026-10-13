package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNormalizePublishInput(t *testing.T) {
	in, err := normalizePublishInput(PublishInput{
		Slug: "  Getting-Started  ", Title: "开始使用", Body: "这是足够长的安全纯文本正文内容。",
		TargetPlatforms: []string{"Android", "web", "android"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if in.Slug != "getting-started" || in.Kind != "kb_article" ||
		in.Locale != "zh-CN" || in.Visibility != "authenticated" || in.Status != "draft" {
		t.Fatalf("normalized input=%+v", in)
	}
	if len(in.TargetPlatforms) != 2 || in.TargetPlatforms[0] != "android" || in.TargetPlatforms[1] != "web" {
		t.Fatalf("platforms=%v", in.TargetPlatforms)
	}
}

func TestNormalizePublishInputRejectsUnsafeShape(t *testing.T) {
	_, err := normalizePublishInput(PublishInput{
		Slug: "../../admin", Kind: "kb_article", Title: "x", Body: "short",
		Visibility: "group", Status: "published", MinClientVersion: "2.0.0", MaxClientVersion: "1.0.0",
	})
	if err == nil {
		t.Fatal("unsafe input accepted")
	}
}

func TestNormalizePublishInputRejectsUnknownPlatform(t *testing.T) {
	_, err := normalizePublishInput(PublishInput{
		Slug: "getting-started", Title: "开始使用", Body: "这是足够长的安全纯文本正文内容。",
		TargetPlatforms: []string{"web", "symbian"},
	})
	if err == nil {
		t.Fatal("unknown platform accepted")
	}
}

func TestNormalizePublishInputRejectsAnonymousPublicVisibility(t *testing.T) {
	_, err := normalizePublishInput(PublishInput{
		Slug: "getting-started", Title: "开始使用", Body: "这是足够长的安全纯文本正文内容。",
		Visibility: "public",
	})
	if err == nil {
		t.Fatal("unsupported anonymous public visibility accepted")
	}
}

func TestVersionVisibility(t *testing.T) {
	for _, tc := range []struct {
		min, max, actual string
		want             bool
	}{
		{"", "", "", true},
		{"1.2.0", "", "1.2.0", true},
		{"1.2.0", "2.0.0", "1.9.9", true},
		{"1.2.0", "2.0.0", "1.1.9", false},
		{"1.2.0", "2.0.0", "2.0.1", false},
		{"1.2.0", "", "", false},
	} {
		if got := versionVisible(tc.min, tc.max, tc.actual); got != tc.want {
			t.Fatalf("versionVisible(%q,%q,%q)=%v want %v", tc.min, tc.max, tc.actual, got, tc.want)
		}
	}
}

func TestNormalizePublishInputRejectsNULInTitleAndBody(t *testing.T) {
	_, err := normalizePublishInput(PublishInput{
		Slug: "ok-slug", Title: "标\x00题", Body: "这是足够长的正文内容用于测试。",
	})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed || e.Fields["title"] == "" {
		t.Fatalf("title NUL: err=%v", err)
	}
	_, err = normalizePublishInput(PublishInput{
		Slug: "ok-slug", Title: "正常标题", Body: "正文\x00里有空字符足够长。",
	})
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed || e.Fields["body"] == "" {
		t.Fatalf("body NUL: err=%v", err)
	}
}

func TestNormalizePublishInputTargetPlanIDs(t *testing.T) {
	bad := uuid.New().String()[:8] + "-not-a-uuid"
	_, err := normalizePublishInput(PublishInput{
		Slug: "plans-test", Title: "套餐测试", Body: "这是足够长的安全纯文本正文内容。",
		TargetPlanIDs: []string{bad},
	})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed || e.Fields["target_plan_ids"] == "" {
		t.Fatalf("bad uuid: err=%v", err)
	}

	id1 := uuid.MustParse("10000000-0000-7000-8000-000000000001")
	id2 := uuid.MustParse("20000000-0000-7000-8000-000000000002")
	in, err := normalizePublishInput(PublishInput{
		Slug: "plans-dedup", Title: "去重测试", Body: "这是足够长的安全纯文本正文内容。",
		TargetPlanIDs: []string{id2.String(), id1.String(), id1.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.TargetPlanIDs) != 2 || in.TargetPlanIDs[0] != id1.String() || in.TargetPlanIDs[1] != id2.String() {
		t.Fatalf("deduped sorted plans=%v", in.TargetPlanIDs)
	}
}

func TestEncodeDecodeList(t *testing.T) {
	if encodeList(nil) != "[]" {
		t.Fatalf("encodeList(nil)=%q", encodeList(nil))
	}
	empty := decodeList("")
	if empty == nil || len(empty) != 0 {
		t.Fatalf("decodeList empty=%v", empty)
	}
	orig := []string{"a", "b"}
	got := decodeList(encodeList(orig))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("round trip=%v", got)
	}
}

func TestCompareVersion(t *testing.T) {
	if compareVersion("v1.2", "1.2.0") != 0 {
		t.Fatalf("v1.2 vs 1.2.0")
	}
	if compareVersion("1.10.0", "1.9.9") <= 0 {
		t.Fatalf("1.10.0 should be > 1.9.9")
	}
}

func TestPublishVersionInvalidInputNoPanic(t *testing.T) {
	ctx := context.Background()
	_, err := (&Service{}).PublishVersion(ctx, PublishInput{
		Slug: "bad", Title: "x", Body: "short",
	})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
		t.Fatalf("want 422 before DB: %v", err)
	}
}
