// [INPUT]: 依赖 ./webapp.go 的 Handler / Mount，依赖 testing/fstest 的 MapFS 模拟构建产物
// [OUTPUT]: 对外提供 webapp 下发契约测试：入口缓存与 CSP、资源 immutable 与 MIME、根下非产物路径与隐藏/穿越路径 404、挂载只注册读方法
// [POS]: platform/webapp 的行为守卫，不依赖真实 Vite 产物；真实产物的可嵌入性由 panel/web 的 app_test.go 守
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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

type recordedRoutes []string

func (r *recordedRoutes) Get(pattern string, _ http.HandlerFunc)  { *r = append(*r, "GET "+pattern) }
func (r *recordedRoutes) Head(pattern string, _ http.HandlerFunc) { *r = append(*r, "HEAD "+pattern) }

func TestMountRegistersOnlyReadMethodsOnRootAndAssets(t *testing.T) {
	var got recordedRoutes
	Mount(&got, fstest.MapFS{})
	want := "GET /,HEAD /,GET /assets/*,HEAD /assets/*"
	if strings.Join(got, ",") != want {
		t.Fatalf("routes = %v, want %s", got, want)
	}
}
