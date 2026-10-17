package webapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func testApp() http.Handler {
	return Handler(fstest.MapFS{
		"index.html":                {Data: []byte(`<!doctype html><script type="module" src="./assets/index-abc.js"></script>`)},
		"assets/index-abc.js":       {Data: []byte("console.log(1)")},
		"assets/index-abc.css":      {Data: []byte("body{}")},
		"assets/font-abc.woff2":     {Data: []byte("wOF2")},
		"assets/blob-abc.unknown":   {Data: []byte("x")},
		".vite/manifest.json":       {Data: []byte("{}")},
		"assets/.hidden.js":         {Data: []byte("x")},
		"favicon.svg":               {Data: []byte("<svg/>")},
		"assets/nested/chunk-a.mjs": {Data: []byte("export{}")},
	})
}

func serve(h http.Handler, method, target string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	// 模拟全局 SecurityHeaders 先写的 no-store，确认下发器会覆盖它
	rec.Header().Set("Cache-Control", "no-store")
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexIsRevalidatedAndCarriesCSP(t *testing.T) {
	h := testApp()
	rec := serve(h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ status = %d", rec.Code)
	}
	hd := rec.Header()
	if hd.Get("Cache-Control") != indexCache {
		t.Fatalf("Cache-Control = %q", hd.Get("Cache-Control"))
	}
	if !strings.HasPrefix(hd.Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type = %q", hd.Get("Content-Type"))
	}
	csp := hd.Get("Content-Security-Policy")
	// 脚本与样式都只许同源：任何 'unsafe-inline' 回潮都算回归
	if strings.Contains(csp, "'unsafe-inline'") {
		t.Fatalf("CSP must not allow inline script or style: %q", csp)
	}
	// 字体 MIME 已登记，CSP 必须放行同源字体，否则两者互相矛盾
	for _, want := range []string{"script-src 'self';", "style-src 'self';", "font-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP lacks %s: %q", want, csp)
		}
	}

	etag := hd.Get("ETag")
	if etag == "" {
		t.Fatal("index has no ETag")
	}
	if rec := serve(h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", rec.Code)
	}
}

func TestAssetsAreImmutableWithExplicitMIME(t *testing.T) {
	h := testApp()
	cases := map[string]string{
		"/assets/index-abc.js":       "text/javascript; charset=utf-8",
		"/assets/index-abc.css":      "text/css; charset=utf-8",
		"/assets/font-abc.woff2":     "font/woff2",
		"/assets/blob-abc.unknown":   "application/octet-stream",
		"/assets/nested/chunk-a.mjs": "text/javascript; charset=utf-8",
	}
	for target, want := range cases {
		rec := serve(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != want {
			t.Fatalf("%s Content-Type = %q, want %q", target, got, want)
		}
		if got := rec.Header().Get("Cache-Control"); got != assetCache {
			t.Fatalf("%s Cache-Control = %q, want immutable", target, got)
		}
	}
}

func TestHeadServesHeadersWithoutBody(t *testing.T) {
	rec := serve(testApp(), http.MethodHead, "/assets/index-abc.js", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status %d body %d bytes", rec.Code, rec.Body.Len())
	}
}

// 根上只有入口与 assets/ 对外：产物根下的其他文件、元数据、隐藏文件、穿越路径一律 404，且保持全局 no-store
func TestNonAssetHiddenAndTraversalPathsAre404(t *testing.T) {
	h := testApp()
	for _, target := range []string{
		"/index.html",
		"/favicon.svg",
		"/assets/missing.js",
		"/assets",
		"/assets/",
		"/.vite/manifest.json",
		"/assets/.hidden.js",
		"/assets/../index.html",
		"/assets//index-abc.js",
		"/assetsx/index-abc.js",
	} {
		rec := serve(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s 404 must keep no-store, got %q", target, got)
		}
	}
}

func TestMissingIndexIs404NotPanic(t *testing.T) {
	h := Handler(fstest.MapFS{"assets/a.js": {Data: []byte("1")}})
	if rec := serve(h, http.MethodGet, "/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// 和订阅通配 /:prefix/:token 挂在同一棵 gin 路由树上：静态段 assets 不抢订阅链接，订阅也不抢产物。
func TestMountCoexistsWithSubscriptionWildcard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/:prefix/:token", func(c *gin.Context) { c.String(http.StatusOK, "sub:"+c.Param("prefix")) })
	Mount(e, fstest.MapFS{
		"index.html":                {Data: []byte("<!doctype html>")},
		"assets/index-abc.js":       {Data: []byte("console.log(1)")},
		"assets/nested/chunk-a.mjs": {Data: []byte("export{}")},
	})
	cases := []struct {
		method, target string
		code           int
		body           string
	}{
		{http.MethodGet, "/", http.StatusOK, "<!doctype html>"},
		{http.MethodHead, "/", http.StatusOK, ""},
		{http.MethodGet, "/assets/index-abc.js", http.StatusOK, "console.log(1)"},
		{http.MethodGet, "/assets/nested/chunk-a.mjs", http.StatusOK, "export{}"},
		{http.MethodGet, "/assets/missing.js", http.StatusNotFound, ""},
		{http.MethodGet, "/s3cr3t/tok", http.StatusOK, "sub:s3cr3t"},
		{http.MethodPost, "/", http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
		if rec.Code != tc.code {
			t.Fatalf("%s %s: status %d, want %d", tc.method, tc.target, rec.Code, tc.code)
		}
		if tc.body != "" && rec.Body.String() != tc.body {
			t.Fatalf("%s %s: body %q, want %q", tc.method, tc.target, rec.Body.String(), tc.body)
		}
	}
}

// 额外来源：不给或给空时 CSP 与 indexCSP 逐字一致；给了只改 script-src、加 frame-src，其余不变，ETag 随之变。
func TestIndexCSPExtraSources(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>`)}}

	plain := serve(Handler(fsys), http.MethodGet, "/", nil)
	if got := plain.Header().Get("Content-Security-Policy"); got != indexCSP {
		t.Fatalf("no option: CSP = %q", got)
	}
	empty := serve(Handler(fsys, WithExtraSources(func(context.Context) ExtraSources { return ExtraSources{} })), http.MethodGet, "/", nil)
	if got := empty.Header().Get("Content-Security-Policy"); got != indexCSP {
		t.Fatalf("empty extra: CSP = %q", got)
	}
	if empty.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Fatalf("empty extra changed ETag: %q vs %q", empty.Header().Get("ETag"), plain.Header().Get("ETag"))
	}

	x := ExtraSources{Script: []string{"https://a.example.test", "https://b.example.test"}, Frame: []string{"https://a.example.test"}}
	h := Handler(fsys, WithExtraSources(func(context.Context) ExtraSources { return x }))
	rec := serve(h, http.MethodGet, "/", nil)
	want := "default-src 'none'; script-src 'self' https://a.example.test https://b.example.test; " +
		"frame-src https://a.example.test; " + indexCSPRest
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("extra CSP:\n got %q\nwant %q", got, want)
	}
	etag := rec.Header().Get("ETag")
	if etag == plain.Header().Get("ETag") || !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Fatalf("extra ETag = %q, plain = %q", etag, plain.Header().Get("ETag"))
	}
	if rec := serve(h, http.MethodGet, "/", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation with extra ETag = %d, want 304", rec.Code)
	}
	// 只放行脚本、不放行 iframe 时不生成 frame-src
	onlyScript := serve(Handler(fsys, WithExtraSources(func(context.Context) ExtraSources {
		return ExtraSources{Script: []string{"https://a.example.test"}}
	})), http.MethodGet, "/", nil)
	if got := onlyScript.Header().Get("Content-Security-Policy"); strings.Contains(got, "frame-src") {
		t.Fatalf("script-only extra must not add frame-src: %q", got)
	}
}
