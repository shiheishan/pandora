package web

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	inlineStyle = regexp.MustCompile(`(?is)<style\b|\sstyle\s*=`)
	assetRef    = regexp.MustCompile(`(?:src|href)="\./([^"]+)"`)
	placeholder = `<meta name="pandora-placeholder" content="unbuilt" />`

	firstScreenScript = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc="\./([^"]+)"`)
	firstScreenLink   = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	linkHref          = regexp.MustCompile(`(?is)\bhref="\./([^"]+)"`)
	linkRel           = regexp.MustCompile(`(?is)\brel\s*=\s*["']([^"']+)["']`)
)

// 首屏预算（未压缩字节）：登录页会下载的 JS / CSS；外框与各页面走懒加载分包，不算在内。
// 2026-10-20 实测后台 JS 383974 / CSS 27853，门户 JS 423494 / CSS 124375（其中约 95KB 是展示字体的 @font-face）；
// 两边 JS 的大头都是 react-dom、zod、query-core 这层底座。超了先看是不是有模块被静态 import 进了登录路径。
var firstScreenBudget = map[string]struct{ js, css int64 }{
	"admin":  {js: 400_000, css: 40_000},
	"portal": {js: 440_000, css: 135_000},
}

func TestEmbeddedAppsHaveEntryForTheirDomain(t *testing.T) {
	for domain, app := range map[string]fs.FS{"admin": AdminApp, "portal": PortalApp} {
		raw, err := fs.ReadFile(app, "index.html")
		if err != nil {
			t.Fatalf("%s: index.html missing from embed: %v", domain, err)
		}
		index := string(raw)
		if !strings.Contains(index, `<meta name="pandora-app" content="`+domain+`"`) {
			t.Fatalf("%s: entry declares the wrong pandora-app domain", domain)
		}
		if strings.Contains(index, placeholder) {
			t.Logf("%s: placeholder entry embedded; run make frontend-embed to check the real build", domain)
			continue
		}
		checkBuiltEntry(t, domain, app, index)
		// 服务端首页片段插在 <div id="root"> 之前（webapp.insertBeforeRoot）；标记写法一变首页会静默消失
		if domain == "portal" && strings.Count(index, `<div id="root">`) != 1 {
			t.Fatalf("portal: entry must contain exactly one <div id=\"root\"> for the home fragment")
		}
	}
}

// checkBuiltEntry 守三条部署前提：
// webapp 的 CSP 是 script-src 'self' 与 style-src 'self'，入口里任何内联脚本或内联样式都会被浏览器拒绝；
// 入口引用的每个 ./ 资源都必须在 assets/ 下并且真的编进了二进制——webapp 只下发 / 与 /assets/*，
// 引用到根下别的文件，上线后就是 404 白屏。
func checkBuiltEntry(t *testing.T, domain string, app fs.FS, index string) {
	t.Helper()
	for _, tag := range scriptTag.FindAllStringSubmatch(index, -1) {
		if !strings.Contains(tag[1], "src=") {
			t.Fatalf("%s: inline <script> would be blocked by script-src 'self': %s", domain, tag[0])
		}
	}
	if match := inlineStyle.FindString(index); match != "" {
		t.Fatalf("%s: inline style would be blocked by style-src 'self': %q", domain, match)
	}
	refs := assetRef.FindAllStringSubmatch(index, -1)
	if len(refs) == 0 {
		t.Fatalf("%s: built entry references no ./ assets; is vite base still './'?", domain)
	}
	for _, ref := range refs {
		if !strings.HasPrefix(ref[1], "assets/") {
			t.Fatalf("%s: entry references %s outside assets/, which webapp never serves", domain, ref[1])
		}
		if _, err := fs.Stat(app, ref[1]); err != nil {
			t.Fatalf("%s: entry references %s but it is not embedded: %v", domain, ref[1], err)
		}
	}
	if _, err := fs.Stat(app, ".vite"); err == nil {
		t.Fatalf("%s: .vite build metadata must not be embedded", domain)
	}
}

func TestEmbeddedEntryFirstScreenBudget(t *testing.T) {
	for domain, app := range map[string]fs.FS{"admin": AdminApp, "portal": PortalApp} {
		raw, err := fs.ReadFile(app, "index.html")
		if err != nil {
			t.Fatalf("%s: index.html missing from embed: %v", domain, err)
		}
		index := string(raw)
		if strings.Contains(index, placeholder) {
			t.Logf("%s: placeholder entry embedded; run make frontend-embed to check the real build", domain)
			continue
		}
		js, css, details := sumFirstScreenAssets(t, app, index)
		budget := firstScreenBudget[domain]
		if js <= budget.js && css <= budget.css {
			continue
		}
		t.Fatalf("%s: 首屏体积超预算 — JS %d (预算 %d), CSS %d (预算 %d)\n%s",
			domain, js, budget.js, css, budget.css, strings.Join(details, "\n"))
	}
}

func sumFirstScreenAssets(t *testing.T, app fs.FS, index string) (jsTotal, cssTotal int64, details []string) {
	t.Helper()
	paths := firstScreenAssetPaths(index)
	var jsFiles, cssFiles []string
	for _, p := range paths {
		info, err := fs.Stat(app, p)
		if err != nil {
			t.Fatalf("first-screen asset %s not embedded: %v", p, err)
		}
		size := info.Size()
		switch strings.ToLower(path.Ext(p)) {
		case ".js":
			jsTotal += size
			jsFiles = append(jsFiles, fmt.Sprintf("  %s  %d", p, size))
		case ".css":
			cssTotal += size
			cssFiles = append(cssFiles, fmt.Sprintf("  %s  %d", p, size))
		}
	}
	sort.Strings(jsFiles)
	sort.Strings(cssFiles)
	if len(jsFiles) > 0 {
		details = append(details, fmt.Sprintf("JS 合计 %d:", jsTotal))
		details = append(details, jsFiles...)
	}
	if len(cssFiles) > 0 {
		details = append(details, fmt.Sprintf("CSS 合计 %d:", cssTotal))
		details = append(details, cssFiles...)
	}
	return jsTotal, cssTotal, details
}

func firstScreenAssetPaths(index string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, m := range firstScreenScript.FindAllStringSubmatch(index, -1) {
		add(m[1])
	}
	for _, tag := range firstScreenLink.FindAllString(index, -1) {
		relM := linkRel.FindStringSubmatch(tag)
		if relM == nil {
			continue
		}
		rel := relM[1]
		if rel != "modulepreload" && rel != "stylesheet" {
			continue
		}
		hrefM := linkHref.FindStringSubmatch(tag)
		if hrefM == nil {
			continue
		}
		add(hrefM[1])
	}
	return out
}
