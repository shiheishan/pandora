// [INPUT]: 依赖 ./app.go 的 AdminApp / PortalApp
// [OUTPUT]: 对外提供前端嵌入产物契约测试：入口存在且域标记正确；真实产物无内联脚本与内联样式、引用的资源全部在 assets/ 内
// [POS]: web 的嵌入完整性守卫：占位页时只查入口，make frontend-embed 之后同一份测试会检查真实 Vite 产物，CI 两种形态都跑
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	inlineStyle = regexp.MustCompile(`(?is)<style\b|\sstyle\s*=`)
	assetRef    = regexp.MustCompile(`(?:src|href)="\./([^"]+)"`)
	placeholder = `<meta name="pandora-placeholder" content="unbuilt" />`
)

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
