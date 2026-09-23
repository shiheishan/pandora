package content

import (
	"encoding/json"
	"testing"
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

func TestPublishInputJSONContract(t *testing.T) {
	var in PublishInput
	if err := json.Unmarshal([]byte(`{"slug":"guide","title":"使用指南","body":"这是足够长的正文内容用于测试。","target_platforms":["web"],"min_client_version":"1.0.0","max_client_version":"2.0.0","target_plan_ids":["10000000-0000-7000-8000-000000000001"],"review_due_at":"2026-12-01T00:00:00Z","expected_latest_version":3}`), &in); err != nil {
		t.Fatal(err)
	}
	if len(in.TargetPlatforms) != 1 || len(in.TargetPlanIDs) != 1 || in.MinClientVersion != "1.0.0" ||
		in.MaxClientVersion != "2.0.0" || in.ReviewDueAt == nil || in.ExpectedLatestVersion != 3 {
		t.Fatalf("underscore JSON fields were not decoded: %+v", in)
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
