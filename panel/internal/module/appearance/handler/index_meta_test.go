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

	// 1. PageMeta{SiteName: "夜海加速", Tagline: "一路畅通", Logo: "data:image/png;base64,AAAA"} + PagePortal
	src1 := fakeMeta{
		SiteName: "夜海加速",
		Tagline:  "一路畅通",
		Logo:     "data:image/png;base64,AAAA",
	}
	f1 := IndexMeta(src1, PagePortal)
	got1 := f1(ctx)
	want1 := webapp.IndexMeta{
		Title:       "夜海加速",
		Description: "一路畅通",
		AppName:     "夜海加速",
		Icon:        "data:image/png;base64,AAAA",
	}
	if got1 != want1 {
		t.Fatalf("got %+v, want %+v", got1, want1)
	}

	// 2. 同一份 + PageAdmin → Title == "夜海加速 控制台"，其余同上
	f2 := IndexMeta(src1, PageAdmin)
	got2 := f2(ctx)
	want2 := webapp.IndexMeta{
		Title:       "夜海加速 控制台",
		Description: "一路畅通",
		AppName:     "夜海加速",
		Icon:        "data:image/png;base64,AAAA",
	}
	if got2 != want2 {
		t.Fatalf("got %+v, want %+v", got2, want2)
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

	// 4. Logo: "https://evil.test/a.png" → Icon == DefaultIcon
	src4 := fakeMeta{
		SiteName: "测试",
		Logo:     "https://evil.test/a.png",
	}
	f4 := IndexMeta(src4, PagePortal)
	got4 := f4(ctx)
	if got4.Icon != DefaultIcon {
		t.Fatalf("icon: got %q, want DefaultIcon", got4.Icon)
	}

	// 5. DefaultIcon 以 data:image/svg+xml;base64, 开头，base64 解码后含 <svg
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
}
