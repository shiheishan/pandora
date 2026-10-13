package handler

// 节点端一键安装的公开路由：内容类型、缓存头、白名单与缺文件的 404 信封，
// 以及和订阅拉取的根路由 /:prefix/:token 挂在同一个引擎上不冲突。

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

const pdndTestBinary = "amd64-bin\n"

// newPdndEngine 挂上 /pdnd 路由与一条模拟的订阅拉取路由；分发目录里只有 amd64。
func newPdndEngine(t *testing.T, baseURL string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, service.PdndArtifactAMD64), []byte(pdndTestBinary), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := service.New(nil, nil, nil, nil)
	svc.UsePdndDist(dir, baseURL)
	New(svc).RegisterRoot(e)
	e.GET("/:prefix/:token", func(c *gin.Context) { c.String(http.StatusOK, "sub") })
	return e
}

func TestPdndInstallScriptRoute(t *testing.T) {
	w := doPool(newPdndEngine(t, "https://panel.example.com"), http.MethodGet, "/pdnd/install.sh", "")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Fatalf("Content-Type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	body := w.Body.String()
	if !strings.Contains(body, "https://panel.example.com") || strings.Contains(body, "@@PANEL_ORIGIN@@") {
		t.Fatal("脚本里的面板地址没有渲染")
	}
}

func TestPdndInstallScriptUnsafeOrigin(t *testing.T) {
	w := doPool(newPdndEngine(t, "http://panel.example.com"), http.MethodGet, "/pdnd/install.sh", "")
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestPdndBinaryRoute(t *testing.T) {
	e := newPdndEngine(t, "https://panel.example.com")
	w := doPool(e, http.MethodGet, "/pdnd/bin/"+service.PdndArtifactAMD64, "")
	if w.Code != http.StatusOK || w.Body.String() != pdndTestBinary {
		t.Fatalf("状态码 %d body %q", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if w.Header().Get("Last-Modified") == "" {
		t.Fatal("缺 Last-Modified（no-cache 靠它重验）")
	}

	// 白名单里但磁盘上没有：404 信封，不是 500
	poolExpectErr(t, doPool(e, http.MethodGet, "/pdnd/bin/"+service.PdndArtifactARM64, ""), errs.NotFound, "")
	for _, name := range []string{"pandora-native-linux-386", "config.json"} {
		poolExpectErr(t, doPool(e, http.MethodGet, "/pdnd/bin/"+name, ""), errs.NotFound, "")
	}
}

func TestPdndChecksumRoute(t *testing.T) {
	e := newPdndEngine(t, "https://panel.example.com")
	w := doPool(e, http.MethodGet, "/pdnd/sha256/"+service.PdndArtifactAMD64, "")
	want := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(pdndTestBinary)), service.PdndArtifactAMD64)
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Fatalf("状态码 %d body %q，期望 %q", w.Code, w.Body, want)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type %q", ct)
	}
	poolExpectErr(t, doPool(e, http.MethodGet, "/pdnd/sha256/"+service.PdndArtifactARM64, ""), errs.NotFound, "")
	poolExpectErr(t, doPool(e, http.MethodGet, "/pdnd/sha256/pandora-native-linux-386", ""), errs.NotFound, "")
}

// 和订阅拉取的 /:prefix/:token 共存：两段的其他路径照旧落到订阅。
func TestPdndRoutesCoexistWithSubscription(t *testing.T) {
	w := doPool(newPdndEngine(t, "https://panel.example.com"), http.MethodGet, "/abc/def", "")
	if w.Code != http.StatusOK || w.Body.String() != "sub" {
		t.Fatalf("状态码 %d body %q", w.Code, w.Body)
	}
}
