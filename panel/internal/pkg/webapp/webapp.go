// Package webapp 把面板前端的构建产物从二进制里下发，挂在门户与后台两个引擎的根上。
//
// 为什么不用 http.FileServerFS：
//
//	· 它会列目录、会把 /index.html 重定向成 ./、会按系统 mime 表猜类型，
//	  三件事在静态二进制 + nginx 高熵前缀的部署里都是隐患；
//	· 缓存策略要按文件区分：入口页每次回源校验，带 hash 的资源一年不变。
//
// 只认两类路径：入口 / 与 Vite 产物目录 /assets/*。前端用 hash 路由，业务路径永远不会打到服务端；
// 不做 SPA 回退，缺失的资源就该是 404，而不是一张 200 的入口页掩盖构建错配。
// 入口页可按 WithIndexMeta 改写 <head>（title、description、application-name、icon），只改文字、不加脚本与样式，CSP 不变。
// 根下其余路径（/v1、/healthz、订阅通配 /:prefix/:token、/pdnd/...）继续归各自的路由，互不抢占：
// gin 的路由树里静态段 assets 优先于参数段，订阅前缀恰好叫 assets 的链接会被遮住（前缀是随机串，碰不上）。
package webapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

//------------------------------------------------------------------------------
// 响应头策略
//------------------------------------------------------------------------------

// indexCSP 对脚本与样式都只放行同源：
//   - Vite 产物没有内联脚本，由 panel/web 的 app_test.go 对真实产物验证；
//   - 前端不用 CSS-in-JS，样式全部在构建出的 .css 文件里；React 的 style 属性走 CSSOM 赋值，不受 style-src 约束。
//
// 每一类放行都与 contentTypes 对应：图片走 img-src，字体走 font-src，json/txt 由脚本 fetch 走 connect-src；
// 登记了 MIME 却不放行，产物一带上这类文件浏览器就会拒绝加载。wasm 因此不登记：编译它需要
// script-src 'wasm-unsafe-eval'，真有需要时两处一起加。
const indexCSP = "default-src 'none'; script-src 'self'; " + indexCSPRest

// indexCSPRest 是 script-src 之后的部分；追加来源时只改 script-src、connect-src、加 frame-src，其余逐字不变。
const indexCSPRest = "style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// ExtraSources 是入口页 CSP 额外放行的来源（如门户开了人机验证时第三方的脚本与 iframe）。
// 本包不读设置：由装配处按设置给出。Script、Frame 只应是装配处写死的固定值；Connect 可以来自后台登记的
// 订阅域名，但装配处必须先按严格形状（https://主机[:端口]）过滤，不能把任意输入拼进响应头。
type ExtraSources struct {
	Script  []string // 追加到 script-src
	Frame   []string // 生成 frame-src（default-src 'none' 下缺省不许任何 iframe）
	Connect []string // 追加到 connect-src（门户跨源探测订阅域名）
}

func (x ExtraSources) empty() bool {
	return len(x.Script) == 0 && len(x.Frame) == 0 && len(x.Connect) == 0
}

// cspWith 在 indexCSP 上追加来源；x 为空时与 indexCSP 逐字一致。
func cspWith(x ExtraSources) string {
	if x.empty() {
		return indexCSP
	}
	csp := "default-src 'none'; script-src 'self'"
	if len(x.Script) > 0 {
		csp += " " + strings.Join(x.Script, " ")
	}
	csp += "; "
	if len(x.Frame) > 0 {
		csp += "frame-src " + strings.Join(x.Frame, " ") + "; "
	}
	rest := indexCSPRest
	if len(x.Connect) > 0 {
		rest = strings.Replace(rest, "connect-src 'self'", "connect-src 'self' "+strings.Join(x.Connect, " "), 1)
	}
	return csp + rest
}

const (
	// 入口页引用的资源名随构建变化，必须每次回源校验，否则升级后拿旧入口配新接口
	indexCache = "private, no-cache"
	// 文件名带内容 hash，内容变了名字就变
	assetCache = "public, max-age=31536000, immutable"
)

// assetDir 是 Vite 默认的产物子目录，也是入口之外唯一对外下发的目录。
const assetDir = "assets"

// contentTypes 显式列出构建产物会出现的扩展名。
// 不走 mime.TypeByExtension：它读宿主的 /etc/mime.types，静态二进制换台机器结果就可能变；
// 而 SecurityHeaders 设了 nosniff，类型错了浏览器会直接拒绝执行。
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".avif":  "image/avif",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".txt":   "text/plain; charset=utf-8",
}

//------------------------------------------------------------------------------
// 挂载
//------------------------------------------------------------------------------

// Mount 把 Handler 注册到 / 与 /assets/*filepath，只接 GET/HEAD：
// 静态资源没有写语义，其余方法交给引擎的 NoRoute。
func Mount(r gin.IRoutes, fsys fs.FS, opts ...Option) {
	h := gin.WrapH(Handler(fsys, opts...))
	for _, pattern := range []string{"/", "/" + assetDir + "/*filepath"} {
		r.GET(pattern, h)
		r.HEAD(pattern, h)
	}
}

//------------------------------------------------------------------------------
// Handler
//------------------------------------------------------------------------------

type handler struct {
	fsys  fs.FS
	index []byte
	etag  string
	extra func(ctx context.Context) ExtraSources
	meta  func(ctx context.Context) IndexMeta

	mu       sync.Mutex
	lastMeta IndexMeta
	lastBody []byte
	lastETag string
	hasLast  bool
}

// Option 调整 Handler。
type Option func(*handler)

// WithExtraSources 让入口页每次请求时调 f 取额外放行的 CSP 来源（f 要便宜：入口页每次回源校验，要缓存由 f 自己做）。
// f 返回空时 CSP 与不带本选项时逐字一致。
func WithExtraSources(f func(ctx context.Context) ExtraSources) Option {
	return func(h *handler) { h.extra = f }
}

// Handler 只读下发 fsys：
//
//	/              → index.html，no-cache + ETag + CSP；可选 WithIndexMeta 改写 <head>
//	/assets/<文件> → 按扩展名给类型，一年 immutable
//	其余           → 404；目录、点开头的段、非法路径同样 404
//
// 调用方只应以 GET/HEAD 注册它，通常经 Mount。
func Handler(fsys fs.FS, opts ...Option) http.Handler {
	h := &handler{fsys: fsys}
	for _, o := range opts {
		o(h)
	}
	// 入口页随二进制固定，启动时读一次；缺失时入口返回 404，不 panic ——
	// 这是构建链的问题，不该让整个网关起不来
	if index, err := fs.ReadFile(fsys, "index.html"); err == nil {
		sum := sha256.Sum256(index)
		h.index = index
		h.etag = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		h.serveIndex(w, r)
		return
	}
	rel := strings.TrimPrefix(p, "/")
	if !strings.HasPrefix(rel, assetDir+"/") || !servable(rel) {
		http.NotFound(w, r)
		return
	}
	h.serveAsset(w, r, rel)
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if h.index == nil {
		http.NotFound(w, r)
		return
	}
	body, etag := h.index, h.etag
	if h.meta != nil {
		m := sanitizeMeta(h.meta(r.Context()))
		if !m.zero() {
			body, etag = h.rendered(m)
		}
	}
	csp := indexCSP
	if h.extra != nil {
		if x := h.extra(r.Context()); !x.empty() {
			csp = cspWith(x)
			// CSP 变了 ETag 也要变：否则浏览器拿着旧 ETag 命中 304，可能沿用缓存里的旧响应头
			sum := sha256.Sum256([]byte(csp))
			etag = strings.TrimSuffix(etag, `"`) + "-" + hex.EncodeToString(sum[:4]) + `"`
		}
	}
	hd := w.Header()
	hd.Set("Content-Type", contentTypes[".html"])
	hd.Set("Content-Security-Policy", csp)
	hd.Set("Cache-Control", indexCache)
	// ServeContent 按这里的 ETag 处理 If-None-Match，命中即 304
	hd.Set("ETag", etag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(body))
}

func (h *handler) serveAsset(w http.ResponseWriter, r *http.Request, rel string) {
	f, err := h.fsys.Open(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	// embed.FS 的文件实现了 Seek，直接交给 ServeContent，不必整份读进内存
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(f)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		rs = bytes.NewReader(data)
	}
	hd := w.Header()
	contentType, known := contentTypes[strings.ToLower(path.Ext(rel))]
	if !known {
		contentType = "application/octet-stream"
	}
	hd.Set("Content-Type", contentType)
	hd.Set("Cache-Control", assetCache)
	http.ServeContent(w, r, info.Name(), time.Time{}, rs)
}

// servable 拒绝一切不是「构建产物里的普通文件路径」的输入：
// 非规范路径（含 .. 、空段、尾斜杠）与任何点开头的段（.vite/manifest.json 之类）。
func servable(rel string) bool {
	if !fs.ValidPath(rel) {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}
