package webapp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"golang.org/x/net/html"
)

const homeIndex = "<!doctype html>\n<html>\n  <head>\n    <meta charset=\"UTF-8\" />\n    <title>Pandora · 用户中心</title>\n  </head>\n  <body>\n    <div id=\"root\"></div>\n    <script type=\"module\" src=\"./assets/index.js\"></script>\n  </body>\n</html>"

func sampleHome() Home {
	return Home{
		SiteName: "夜海加速",
		Tagline:  "虚构的测试站",
		Signup:   SignupOpen,
		Intro:    "第一段\n\n第二段",
		Plans: []HomePlan{
			{
				Name:      "入门版",
				Kind:      "按时间",
				Badge:     "推荐",
				Usage:     "每期 100G · 3 台设备",
				Price:     "¥12.00 / 月",
				PriceNote: "季付 ¥33.00 · 年付 ¥108.00",
			},
			{
				Name:      "畅游一次性",
				Kind:      "一次性",
				Usage:     "共 500G · 不限台数",
				Price:     "¥50.00",
				PriceNote: "有效 30 天",
			},
		},
		FAQ: []HomeFAQ{
			{Q: "怎么注册？", A: "点注册填邮箱。"},
			{Q: "支持哪些设备？", A: "常见手机与电脑。"},
		},
	}
}

func TestRenderHomeNoInlineScriptStyleOrHandlers(t *testing.T) {
	h := Home{
		SiteName: "夜海<script>alert(1)</script>",
		Tagline:  "\"><style>body{}</style>",
		Plans: []HomePlan{
			{Name: "<b onclick=\"x()\">", Kind: "按时间", Price: "¥1"},
		},
		FAQ: []HomeFAQ{
			{Q: "<img src=x onerror=alert(1)>", A: "javascript:alert(1)\n<iframe>"},
		},
	}
	out := renderHome(sanitizeHome(h))
	assertNoActiveContent(t, out)
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("expected escaped script in output: %s", out)
	}
}

func TestRenderHomeStructure(t *testing.T) {
	out := renderHome(sanitizeHome(sampleHome()))
	assertNoActiveContent(t, out)
	for _, want := range []string{
		`id="ph-home"`,
		`<h1 id="ph-title" class="ph-title">夜海加速</h1>`,
		`<p class="ph-intro">第一段</p>`,
		`<p class="ph-intro">第二段</p>`,
		`href="#/login"`,
		`href="#/register"`,
		`aria-label="购买入门版"`,
		`<details class="ph-qa">`,
		`ph-plan-rec`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ph-cta-note") {
		t.Fatalf("open signup must not contain ph-cta-note:\n%s", out)
	}

	noPlans := sampleHome()
	noPlans.Plans = nil
	outNoPlans := renderHome(sanitizeHome(noPlans))
	if strings.Contains(outNoPlans, "ph-plans") {
		t.Fatal("empty plans must omit ph-plans section")
	}

	noFAQ := sampleHome()
	noFAQ.FAQ = nil
	outNoFAQ := renderHome(sanitizeHome(noFAQ))
	if strings.Contains(outNoFAQ, "ph-faq") {
		t.Fatal("empty FAQ must omit ph-faq section")
	}
}

func TestRenderHomeSignup(t *testing.T) {
	cases := []struct {
		name     string
		home     Home
		contains []string
		omits    []string
	}{
		{
			name: "open",
			home: func() Home {
				h := sampleHome()
				h.Signup = SignupOpen
				return h
			}(),
			contains: []string{`href="#/register"`, `aria-label="购买入门版"`},
			omits:    []string{"ph-cta-note", "登录后购买"},
		},
		{
			name: "invite",
			home: func() Home {
				h := sampleHome()
				h.Signup = SignupInvite
				return h
			}(),
			contains: []string{`href="#/register"`, "注册需要邀请码，可向好友要", `aria-label="购买入门版"`},
			omits:    []string{"登录后购买"},
		},
		{
			name: "closed",
			home: Home{
				SiteName: "夜海加速",
				Tagline:  "虚构的测试站",
				Plans:    sampleHome().Plans,
				FAQ:      sampleHome().FAQ,
			},
			contains: []string{"注册已关闭，已有账号请直接登录", `aria-label="登录后购买入门版"`, `href="#/login"`},
			omits:    []string{"#/register", ">注册<"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderHome(sanitizeHome(tc.home))
			assertNoActiveContent(t, out)
			if strings.Contains(out, "<img") {
				t.Fatalf("must not contain <img: %s", out)
			}
			for _, s := range tc.contains {
				if !strings.Contains(out, s) {
					t.Fatalf("missing %q in:\n%s", s, out)
				}
			}
			for _, s := range tc.omits {
				if strings.Contains(out, s) {
					t.Fatalf("unexpected %q in:\n%s", s, out)
				}
			}
		})
	}

	t.Run("invalid_signup_defaults_to_closed", func(t *testing.T) {
		h := sampleHome()
		h.Signup = Signup(99)
		san := sanitizeHome(h)
		if san.Signup != SignupClosed {
			t.Fatalf("expected SignupClosed, got %v", san.Signup)
		}
		out := renderHome(san)
		assertNoActiveContent(t, out)
		if strings.Contains(out, "<img") {
			t.Fatalf("must not contain <img: %s", out)
		}
		if strings.Contains(out, "#/register") || strings.Contains(out, ">注册<") {
			t.Fatalf("invalid signup must not contain register: %s", out)
		}
		if !strings.Contains(out, "注册已关闭，已有账号请直接登录") {
			t.Fatalf("invalid signup must contain closed notice: %s", out)
		}
		if !strings.Contains(out, `aria-label="登录后购买入门版"`) {
			t.Fatalf("invalid signup must contain login-to-buy aria-label: %s", out)
		}
	})
}

func TestHandlerWithHome(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(homeIndex)}}
	plain := serve(Handler(fsys), http.MethodGet, "/", nil)
	plainBody := plain.Body.String()
	plainETag := plain.Header().Get("ETag")

	hFalse := Handler(fsys, WithHome(func(context.Context) (Home, bool) { return Home{}, false }))
	recFalse := serve(hFalse, http.MethodGet, "/", nil)
	if recFalse.Body.String() != plainBody {
		t.Fatal("false home body must match plain")
	}
	if recFalse.Header().Get("ETag") != plainETag {
		t.Fatal("false home ETag must match plain")
	}

	hBlank := Handler(fsys, WithHome(func(context.Context) (Home, bool) { return Home{SiteName: "  "}, true }))
	recBlank := serve(hBlank, http.MethodGet, "/", nil)
	if recBlank.Body.String() != plainBody {
		t.Fatal("blank site name body must match plain")
	}
	if recBlank.Header().Get("ETag") != plainETag {
		t.Fatal("blank site name ETag must match plain")
	}

	home := sampleHome()
	h := Handler(fsys, WithHome(func(context.Context) (Home, bool) { return home, true }))
	rec := serve(h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	idxHome := strings.Index(body, `id="ph-home"`)
	idxRoot := strings.Index(body, `<div id="root"></div>`)
	if idxHome < 0 || idxRoot < 0 || idxHome >= idxRoot {
		t.Fatalf("ph-home must appear before root: home=%d root=%d\n%s", idxHome, idxRoot, body)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || etag == plainETag {
		t.Fatalf("home ETag %q must differ from plain %q", etag, plainETag)
	}
	etag2 := serve(h, http.MethodGet, "/", nil).Header().Get("ETag")
	if etag2 != etag {
		t.Fatalf("stable ETag: %q vs %q", etag, etag2)
	}

	var n atomic.Int32
	hChg := Handler(fsys, WithHome(func(context.Context) (Home, bool) {
		h := sampleHome()
		if n.Add(1) == 2 {
			h.FAQ[1].A = "改了答案"
		}
		return h, true
	}))
	e1 := serve(hChg, http.MethodGet, "/", nil).Header().Get("ETag")
	e2 := serve(hChg, http.MethodGet, "/", nil).Header().Get("ETag")
	if e1 == e2 {
		t.Fatalf("ETag must change when FAQ changes: %q vs %q", e1, e2)
	}

	if rec := serve(h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Fatalf("304 with home etag: %d", rec.Code)
	}

	hBoth := Handler(fsys,
		WithIndexMeta(func(context.Context) IndexMeta { return IndexMeta{Title: "夜海加速"} }),
		WithHome(func(context.Context) (Home, bool) { return home, true }),
	)
	bothBody := serve(hBoth, http.MethodGet, "/", nil).Body.String()
	if !strings.Contains(bothBody, "<title>夜海加速</title>") || !strings.Contains(bothBody, `id="ph-home"`) {
		t.Fatalf("meta + home body: %s", bothBody)
	}
	if serve(h, http.MethodGet, "/", nil).Header().Get("Content-Security-Policy") != indexCSP {
		t.Fatal("CSP must stay indexCSP")
	}
}

func TestHandlerHomeDescJoinsLines(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(homeIndex)}}
	home := sampleHome()
	home.Plans[0].Desc = "first line\nsecond line"
	body := serve(Handler(fsys, WithHome(func(context.Context) (Home, bool) { return home, true })), http.MethodGet, "/", nil).Body.String()
	if !strings.Contains(body, "first line second line") {
		t.Fatalf("multi-line desc must join with a space:\n%s", body)
	}
}

func TestInsertBeforeRootMissingMarker(t *testing.T) {
	doc := []byte("<!doctype html><body><div id=\"other\"></div></body>")
	got := insertBeforeRoot(doc, "<div>frag</div>")
	if !bytesEqual(got, doc) {
		t.Fatalf("missing marker must return original: %q vs %q", got, doc)
	}
}

// assertNoActiveContent 按 HTML 词法逐个标签检查：没有能执行或改样式的元素，没有 style 与 on* 属性，图片只来自 data:image/。
// 不用正则扫整段文字：转义后的用户输入里照样有「 onerror=」这样的字样，那是文字不是属性。
func assertNoActiveContent(t *testing.T, out string) {
	t.Helper()
	z := html.NewTokenizer(strings.NewReader(out))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				t.Fatalf("tokenize: %v", z.Err())
			}
			return
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		switch tok.Data {
		case "script", "style", "iframe", "object", "embed", "link", "meta", "base", "form", "b":
			t.Fatalf("must not contain <%s>: %s", tok.Data, out)
		}
		for _, a := range tok.Attr {
			key := strings.ToLower(a.Key)
			if key == "style" || strings.HasPrefix(key, "on") {
				t.Fatalf("<%s> must not carry %s=: %s", tok.Data, a.Key, out)
			}
			if tok.Data == "img" && key == "src" && !strings.HasPrefix(a.Val, "data:image/") {
				t.Fatalf("img src must be data:image/: %q", a.Val)
			}
		}
	}
}
