package service

import (
	"encoding/base64"
	"net/url"
	"testing"
)

func TestNeedsSubscribeURL(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"plain", false},
		{"{{siteName}}", false},
		{"{{subscribeUrl}}", true},
		{"{{urlEncodeSubscribeUrl}}", true},
		{"{{safeBase64SubscribeUrl}}", true},
	}
	for _, tc := range cases {
		if got := NeedsSubscribeURL(tc.body); got != tc.want {
			t.Errorf("NeedsSubscribeURL(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestRenderBody(t *testing.T) {
	link := "https://panel.example.com/s/abc?x=1+2"
	vars := RenderVars{
		SiteName:     "示例站",
		SiteURL:      "https://panel.example.com",
		SubscribeURL: link,
	}
	enc := url.QueryEscape(link)
	b64 := base64.RawURLEncoding.EncodeToString([]byte(link))

	cases := []struct {
		name      string
		body      string
		vars      RenderVars
		hasAccess bool
		want      string
	}{
		{
			name: "五个变量替换",
			body: "{{siteName}}|{{siteUrl}}|{{subscribeUrl}}|{{urlEncodeSubscribeUrl}}|{{safeBase64SubscribeUrl}}",
			vars: vars, hasAccess: true,
			want: "示例站|https://panel.example.com|" + link + "|" + enc + "|" + b64,
		},
		{
			name: "未知变量保留",
			body: "{{foo}}",
			vars: vars, hasAccess: true,
			want: "{{foo}}",
		},
		{
			name: "有权限去标记留内容",
			body: "前<!--access start-->秘<!--access end-->后",
			vars: vars, hasAccess: true,
			want: "前秘后",
		},
		{
			name: "无权限换提示",
			body: "前<!--access start-->秘<!--access end-->后",
			vars: vars, hasAccess: false,
			want: "前" + AccessDeniedNotice + "后",
		},
		{
			name: "两个区块",
			body: "A<!--access start-->1<!--access end-->B<!--access start-->2<!--access end-->C",
			vars: vars, hasAccess: false,
			want: "A" + AccessDeniedNotice + "B" + AccessDeniedNotice + "C",
		},
		{
			name: "未闭合区块藏到末尾",
			body: "头<!--access start-->尾巴",
			vars: vars, hasAccess: false,
			want: "头" + AccessDeniedNotice,
		},
		{
			name: "未闭合有权限",
			body: "头<!--access start-->尾巴",
			vars: vars, hasAccess: true,
			want: "头尾巴",
		},
		{
			name: "落单结束标记被删",
			body: "x<!--access end-->y",
			vars: vars, hasAccess: true,
			want: "xy",
		},
		{
			name: "标记带空白",
			body: "<!-- access start -->内<!-- access end -->",
			vars: vars, hasAccess: true,
			want: "内",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderBody(tc.body, tc.vars, tc.hasAccess)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchHit(t *testing.T) {
	body := "公开 Alpha<!--access start-->Secret 口令<!--access end-->尾巴<!--access start-->未闭合 Hidden"
	cases := []struct {
		q    string
		want bool
	}{
		{"alpha", true},
		{"尾巴", true},
		{"标题", true},
		{"secret", false},
		{"口令", false},
		{"hidden", false},
		{"access", false},
		{"这部分内容", false},
	}
	for _, tc := range cases {
		if got := SearchHit("标题", "摘要", body, tc.q); got != tc.want {
			t.Errorf("SearchHit(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}
