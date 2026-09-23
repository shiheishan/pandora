// [INPUT]: 依赖 ./webapp.go 的 Handler，依赖 testing/fstest 的 MapFS 模拟构建产物
// [OUTPUT]: 对外提供 webapp 下发契约测试：重定向、入口缓存与 CSP、资源 immutable 与 MIME、404 与路径拒绝
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
	return Handler("/app", fstest.MapFS{
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

func TestMountRedirectsRelativeToKeepProxyPrefix(t *testing.T) {
	rec := serve(testApp(), http.MethodGet, "/app", nil)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "app/" {
		t.Fatalf("Location = %q, want relative app/", got)
	}
}

func TestIndexIsRevalidatedAndCarriesCSP(t *testing.T) {
	h := testApp()
	for _, target := range []string{"/app/", "/app/index.html"} {
		rec := serve(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, rec.Code)
		}
		hd := rec.Header()
		if hd.Get("Cache-Control") != indexCache {
			t.Fatalf("%s Cache-Control = %q", target, hd.Get("Cache-Control"))
		}
		if !strings.HasPrefix(hd.Get("Content-Type"), "text/html") {
			t.Fatalf("%s Content-Type = %q", target, hd.Get("Content-Type"))
		}
		csp := hd.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
			t.Fatalf("%s CSP must allow only same-origin scripts: %q", target, csp)
		}
		// 字体 MIME 已登记，CSP 必须放行同源字体，否则两者互相矛盾
		for _, want := range []string{"frame-ancestors 'none'", "font-src 'self'"} {
			if !strings.Contains(csp, want) {
				t.Fatalf("%s CSP lacks %s: %q", target, want, csp)
			}
		}
	}

	etag := serve(h, http.MethodGet, "/app/", nil).Header().Get("ETag")
	if etag == "" {
		t.Fatal("index has no ETag")
	}
	if rec := serve(h, http.MethodGet, "/app/", http.Header{"If-None-Match": {etag}}); rec.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", rec.Code)
	}
}

func TestAssetsAreImmutableWithExplicitMIME(t *testing.T) {
	h := testApp()
	cases := map[string]string{
		"/app/assets/index-abc.js":       "text/javascript; charset=utf-8",
		"/app/assets/index-abc.css":      "text/css; charset=utf-8",
		"/app/assets/font-abc.woff2":     "font/woff2",
		"/app/assets/blob-abc.unknown":   "application/octet-stream",
		"/app/assets/nested/chunk-a.mjs": "text/javascript; charset=utf-8",
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
	rec := serve(h, http.MethodGet, "/app/favicon.svg", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != indexCache {
		t.Fatalf("root file: status %d cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestHeadServesHeadersWithoutBody(t *testing.T) {
	rec := serve(testApp(), http.MethodHead, "/app/assets/index-abc.js", nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status %d body %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestMissingHiddenAndTraversalPathsAre404(t *testing.T) {
	h := testApp()
	for _, target := range []string{
		"/app/assets/missing.js",
		"/app/assets",
		"/app/assets/",
		"/app/.vite/manifest.json",
		"/app/assets/.hidden.js",
		"/app/assets/../index.html",
		"/app/assets//index-abc.js",
		"/application",
		"/other/app/",
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
	h := Handler("/app", fstest.MapFS{"assets/a.js": {Data: []byte("1")}})
	if rec := serve(h, http.MethodGet, "/app/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

type recordedRoutes []string

func (r *recordedRoutes) Get(pattern string, _ http.HandlerFunc)  { *r = append(*r, "GET "+pattern) }
func (r *recordedRoutes) Head(pattern string, _ http.HandlerFunc) { *r = append(*r, "HEAD "+pattern) }

func TestMountRegistersOnlyReadMethodsOnBothPatterns(t *testing.T) {
	var got recordedRoutes
	Mount(&got, "/app", fstest.MapFS{})
	want := "GET /app,HEAD /app,GET /app/*,HEAD /app/*"
	if strings.Join(got, ",") != want {
		t.Fatalf("routes = %v, want %s", got, want)
	}
}
