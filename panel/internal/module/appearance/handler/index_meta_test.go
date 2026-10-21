package handler

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/appearance/service"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
)

type fakeMeta service.PageMeta

func (f fakeMeta) PageMeta(context.Context) service.PageMeta {
	return service.PageMeta(f)
}

func TestIndexMeta(t *testing.T) {
	ctx := context.Background()

	// 1. PageMeta{SiteName: "夜海加速", Tagline: "一路畅通"} + PagePortal
	src1 := fakeMeta{
		SiteName: "夜海加速",
		Tagline:  "一路畅通",
	}
	f1 := IndexMeta(src1, PagePortal)
	got1 := f1(ctx)
	want1 := webapp.IndexMeta{
		Title:       "夜海加速",
		Description: "一路畅通",
		AppName:     "夜海加速",
		Icon:        DefaultIcon,
		Robots:      robotsNoIndex,
		OpenGraph:   true,
	}
	if got1 != want1 {
		t.Fatalf("got %+v, want %+v", got1, want1)
	}

	// 2. 同一份 + PageAdmin → Title == "夜海加速 控制台"，其余同上（无 SEO 字段）
	f2 := IndexMeta(src1, PageAdmin)
	got2 := f2(ctx)
	want2 := webapp.IndexMeta{
		Title:       "夜海加速 控制台",
		Description: "一路畅通",
		AppName:     "夜海加速",
		Icon:        DefaultIcon,
		Robots:      robotsNoIndex,
		OpenGraph:   false,
	}
	if got2 != want2 {
		t.Fatalf("got %+v, want %+v", got2, want2)
	}

	// 门户、后台两种 Page 的 Icon 都等于 DefaultIcon
	if got1.Icon != DefaultIcon || got2.Icon != DefaultIcon {
		t.Fatalf("门户、后台两种 Page 的 Icon 都应等于 DefaultIcon: got portal=%s admin=%s", got1.Icon, got2.Icon)
	}

	// 3. PageMeta{SiteName: "  "} + PagePortal → Title == "Pandora"、Description == "Pandora"、Icon == DefaultIcon
	src3 := fakeMeta{SiteName: "  "}
	f3 := IndexMeta(src3, PagePortal)
	got3 := f3(ctx)
	if got3.Title != "Pandora" {
		t.Fatalf("title: got %q, want %q", got3.Title, "Pandora")
	}
	if got3.Description != "Pandora" {
		t.Fatalf("description: got %q, want %q", got3.Description, "Pandora")
	}
	if got3.AppName != "Pandora" {
		t.Fatalf("appName: got %q, want %q", got3.AppName, "Pandora")
	}
	if got3.Icon != DefaultIcon {
		t.Fatalf("icon: got %q, want DefaultIcon", got3.Icon)
	}
	if got3.Robots != robotsNoIndex {
		t.Fatalf("robots: got %q", got3.Robots)
	}
	if !got3.OpenGraph {
		t.Fatal("portal must enable OpenGraph")
	}

	// 4. DefaultIcon 以 data:image/svg+xml;base64, 开头，base64 解码后含 <svg
	prefix := "data:image/svg+xml;base64,"
	if !strings.HasPrefix(DefaultIcon, prefix) {
		t.Fatalf("DefaultIcon 缺少前缀 %q: %s", prefix, DefaultIcon)
	}
	b64Part := strings.TrimPrefix(DefaultIcon, prefix)
	raw, err := base64.StdEncoding.DecodeString(b64Part)
	if err != nil {
		t.Fatalf("DefaultIcon base64 解码失败: %v", err)
	}
	if !strings.Contains(string(raw), "<svg") {
		t.Fatalf("DefaultIcon 解码后不含 <svg: %s", string(raw))
	}

	// 门户 AllowIndexing: true → Robots 为空
	srcIdx := fakeMeta{SiteName: "站", Tagline: "标", AllowIndexing: true}
	if got := IndexMeta(srcIdx, PagePortal)(ctx); got.Robots != "" {
		t.Fatalf("allow indexing: robots %q", got.Robots)
	}

	// SEODescription 非空盖过标语
	srcSEO := fakeMeta{
		SiteName:       "站",
		Tagline:        "标语",
		SEODescription: "SEO 描述",
	}
	if got := IndexMeta(srcSEO, PagePortal)(ctx); got.Description != "SEO 描述" {
		t.Fatalf("description: got %q", got.Description)
	}

	// 门户带 SEOKeywords、SiteURL；后台不带
	srcKW := fakeMeta{
		SiteName:    "站",
		SEOKeywords: "a, b",
		SiteURL:     "https://portal.example.test/",
	}
	gotP := IndexMeta(srcKW, PagePortal)(ctx)
	if gotP.Keywords != "a, b" || gotP.URL != "https://portal.example.test/" {
		t.Fatalf("portal seo: %+v", gotP)
	}
	gotA := IndexMeta(srcKW, PageAdmin)(ctx)
	if gotA.Keywords != "" || gotA.URL != "" {
		t.Fatalf("admin must not carry keywords/url: %+v", gotA)
	}
}
