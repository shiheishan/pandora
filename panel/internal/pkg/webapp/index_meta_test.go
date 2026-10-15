package webapp

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

const sampleIndex = `<!doctype html>
<html>
  <head>
    <meta charset="UTF-8" />
    <title>Pandora · 用户中心</title>
  </head>
  <body></body>
</html>`

func TestRenderIndexZero(t *testing.T) {
	index := []byte(sampleIndex)
	if got := renderIndex(index, IndexMeta{}); !bytesEqual(got, index) {
		t.Fatal("zero meta must return original bytes")
	}
	if got := renderIndex(index, IndexMeta{Icon: "https://evil.test/a.png"}); !bytesEqual(got, index) {
		t.Fatal("invalid icon only must return original bytes")
	}
}

func TestRenderIndexTypical(t *testing.T) {
	m := IndexMeta{
		Title:       "夜海<加速>",
		Description: "一路\"畅通\"",
		AppName:     "夜海<加速>",
		Icon:        "data:image/png;base64,AAAA",
	}
	out := string(renderIndex([]byte(sampleIndex), m))
	for _, want := range []string{
		"<title>夜海&lt;加速&gt;</title>",
		`<meta name="description" content="一路&#34;畅通&#34;" />`,
		`<meta name="application-name" content="夜海&lt;加速&gt;" />`,
		`<link rel="icon" href="data:image/png;base64,AAAA" />`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Pandora · 用户中心") {
		t.Fatal("old title must be gone")
	}
	if !strings.Contains(out, `<meta charset="UTF-8" />`) {
		t.Fatal("charset meta must remain")
	}
	// 新标签按入口的缩进各占一行，</head> 仍独占一行
	if !strings.Contains(out, "</title>\n    <meta name=\"description\"") || !strings.Contains(out, `<link rel="icon" href="data:image/png;base64,AAAA" />`+"\n  </head>") {
		t.Fatalf("tags must keep the entry's indentation:\n%s", out)
	}
	headEnd := strings.Index(out, "</head>")
	for _, frag := range []string{"description", "application-name", `rel="icon"`} {
		if strings.Index(out, frag) >= headEnd {
			t.Fatalf("%q must appear before </head>", frag)
		}
	}
}

func TestRenderIndexIconRejected(t *testing.T) {
	for _, icon := range []string{"https://evil.test/a.png", "javascript:x"} {
		out := string(renderIndex([]byte(sampleIndex), IndexMeta{Title: "T", Icon: icon}))
		if strings.Contains(out, `rel="icon"`) {
			t.Fatalf("icon %q must not produce link", icon)
		}
	}
}

func TestRenderIndexControlCharsInTitle(t *testing.T) {
	out := string(renderIndex([]byte(sampleIndex), IndexMeta{Title: "a\x00b\nc"}))
	if !strings.Contains(out, "<title>abc</title>") {
		t.Fatalf("title must strip controls: %s", out)
	}
}

func TestRenderIndexNoTitleTag(t *testing.T) {
	index := `<!doctype html><html><head><meta charset="UTF-8" /></head><body></body></html>`
	out := string(renderIndex([]byte(index), IndexMeta{Title: "站点"}))
	if !strings.Contains(out, "<title>站点</title>") {
		t.Fatalf("must insert title: %s", out)
	}
}

// 没有 <title> 也没有 </head>（不是构建产物的形状）：不知道往哪插，原样返回
func TestRenderIndexNoHeadClose(t *testing.T) {
	index := `<!doctype html><html><body></body></html>`
	out := string(renderIndex([]byte(index), IndexMeta{Title: "仅标题", Description: "d"}))
	if out != index {
		t.Fatalf("没有 </head> 时应原样返回：%s", out)
	}
}

func TestHandlerWithIndexMeta(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(sampleIndex)}}
	var calls atomic.Int32
	meta := IndexMeta{Title: "夜海", Description: "desc", AppName: "夜海", Icon: "data:image/png;base64,AA"}
	h := Handler(fsys, WithIndexMeta(func(context.Context) IndexMeta {
		calls.Add(1)
		return meta
	}))
	plain := serve(Handler(fsys), http.MethodGet, "/", nil)

	rec := serve(h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>夜海</title>") {
		t.Fatalf("body %q", body)
	}
	if rec.Header().Get("Content-Security-Policy") != indexCSP {
		t.Fatalf("CSP must match indexCSP: %q", rec.Header().Get("Content-Security-Policy"))
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || etag == plain.Header().Get("ETag") {
		t.Fatalf("meta ETag %q must differ from plain %q", etag, plain.Header().Get("ETag"))
	}
	if rec := serve(h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Fatalf("304 with meta etag: %d", rec.Code)
	}

	// 零值 meta 与不带选项一致
	hZero := Handler(fsys, WithIndexMeta(func(context.Context) IndexMeta { return IndexMeta{} }))
	zero := serve(hZero, http.MethodGet, "/", nil)
	if zero.Body.String() != plain.Body.String() {
		t.Fatal("zero meta body must match plain")
	}
	if zero.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatal("zero meta ETag must match plain")
	}

	// Title 变化则 ETag 变化
	var n atomic.Int32
	hChg := Handler(fsys, WithIndexMeta(func(context.Context) IndexMeta {
		if n.Add(1) == 1 {
			return IndexMeta{Title: "A"}
		}
		return IndexMeta{Title: "B"}
	}))
	e1 := serve(hChg, http.MethodGet, "/", nil).Header().Get("ETag")
	e2 := serve(hChg, http.MethodGet, "/", nil).Header().Get("ETag")
	if e1 == e2 {
		t.Fatalf("ETag must change when meta changes: %q vs %q", e1, e2)
	}
	_ = calls.Load()
}

func TestHandlerIndexMetaWithExtraSources(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(sampleIndex)}}
	metaOnly := Handler(fsys, WithIndexMeta(func(context.Context) IndexMeta {
		return IndexMeta{Title: "X"}
	}))
	both := Handler(fsys,
		WithExtraSources(func(context.Context) ExtraSources {
			return ExtraSources{Script: []string{"https://a.example.test"}}
		}),
		WithIndexMeta(func(context.Context) IndexMeta {
			return IndexMeta{Title: "X"}
		}),
	)
	rec := serve(both, http.MethodGet, "/", nil)
	wantCSP := cspWith(ExtraSources{Script: []string{"https://a.example.test"}})
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Fatalf("CSP\n got %q\nwant %q", got, wantCSP)
	}
	etagBoth := rec.Header().Get("ETag")
	etagMeta := serve(metaOnly, http.MethodGet, "/", nil).Header().Get("ETag")
	if etagBoth == etagMeta {
		t.Fatalf("extra sources must change ETag: %q vs %q", etagBoth, etagMeta)
	}
	if !strings.HasSuffix(etagBoth, `"`) || !strings.Contains(etagBoth, "-") {
		t.Fatalf("ETag should end with hash suffix: %q", etagBoth)
	}
}

func TestHandlerIndexMetaNoExtraScriptOrStyle(t *testing.T) {
	raw := `<!doctype html><head><script type="module" src="/a.js"></script></head><body></body>`
	fsys := fstest.MapFS{"index.html": {Data: []byte(raw)}}
	h := Handler(fsys, WithIndexMeta(func(context.Context) IndexMeta {
		return IndexMeta{Title: "T", Description: "D", AppName: "A", Icon: "data:image/png;base64,x"}
	}))
	body := serve(h, http.MethodGet, "/", nil).Body.String()
	if strings.Count(body, "<script") != strings.Count(raw, "<script") {
		t.Fatalf("script count changed: %q", body)
	}
	if strings.Contains(body, "style=") || strings.Contains(body, "<style") {
		t.Fatalf("must not add style: %q", body)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
