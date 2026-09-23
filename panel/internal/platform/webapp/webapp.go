// [INPUT]: 依赖标准库 io/fs 读取调用方传入的构建产物目录（panel/web 的 AdminApp / PortalApp），依赖 net/http 的 ServeContent
// [OUTPUT]: 对外提供 Handler(mount, fsys) 只读下发一个 Vite 构建产物目录，Mount(r, mount, fsys) 把它以 GET/HEAD 注册到 mount 与 mount/*，Routes 为其最小路由接口
// [POS]: platform 的静态前端托管器，无业务语义；admin 与 public 两个 router 各挂一次 /app，旧单页仍由各自 handlers 在 / 下发
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package webapp 把 React 候选前端的构建产物从二进制里下发。
//
// 为什么不用 http.FileServerFS：
//
//	· 它会列目录、会把 /index.html 重定向成 ./、会按系统 mime 表猜类型，
//	  三件事在静态二进制 + nginx 高熵前缀的部署里都是隐患；
//	· 缓存策略要按文件区分：入口页每次回源校验，带 hash 的资源一年不变。
//
// 不做 SPA 回退：React 用 hash 路由，业务路径永远不会打到服务端，
// 缺失的资源就该是 404，而不是一张 200 的入口页掩盖构建错配。
package webapp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//------------------------------------------------------------------------------
// 响应头策略
//------------------------------------------------------------------------------

// indexCSP 比旧单页更紧：Vite 产物没有内联脚本，script-src 只留 'self'；
// antd 的 CSS-in-JS 会注入 <style>，所以 style-src 仍需 'unsafe-inline'。
// 每一类放行都与 contentTypes 对应：图片走 img-src，字体走 font-src，json/txt 由脚本 fetch 走 connect-src；
// 登记了 MIME 却不放行，产物一带上这类文件浏览器就会拒绝加载。wasm 因此不登记：编译它需要
// script-src 'wasm-unsafe-eval'，真有需要时两处一起加。
const indexCSP = "default-src 'none'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

const (
	// 入口页引用的资源名随构建变化，必须每次回源校验，否则升级后拿旧入口配新接口
	indexCache = "private, no-cache"
	// 文件名带内容 hash，内容变了名字就变
	assetCache = "public, max-age=31536000, immutable"
)

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
// Handler
//------------------------------------------------------------------------------

// Routes 是 Mount 需要的最小路由接口。chi.Router 天然满足，platform 因此不必 import chi。
type Routes interface {
	Get(pattern string, h http.HandlerFunc)
	Head(pattern string, h http.HandlerFunc)
}

// Mount 把 Handler 注册到 mount 与 mount/* 两个模式，只接 GET/HEAD：
// 静态资源没有写语义，其余方法交给路由器的默认 405。
func Mount(r Routes, mount string, fsys fs.FS) {
	h := Handler(mount, fsys).ServeHTTP
	for _, pattern := range []string{mount, mount + "/*"} {
		r.Get(pattern, h)
		r.Head(pattern, h)
	}
}

type handler struct {
	mount string
	fsys  fs.FS
	index []byte
	etag  string
}

// Handler 在 mount（如 "/app"）下只读下发 fsys：
//
//	mount           → 301 到相对地址 "<末段>/"，保留 nginx 前缀
//	mount/          → index.html，no-cache + ETag + CSP
//	mount/assets/*  → 按扩展名给类型，一年 immutable
//	其余文件        → no-cache；目录、点开头的段、非法路径一律 404
//
// 调用方只应以 GET/HEAD 注册它，通常经 Mount。
func Handler(mount string, fsys fs.FS) http.Handler {
	h := &handler{mount: strings.TrimSuffix(mount, "/"), fsys: fsys}
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
	if p == h.mount {
		// 相对 Location 由浏览器按请求 URL 解析：/PREFIX/app → /PREFIX/app/。
		// 不用 http.Redirect，它会按 Go 看到的路径补成绝对地址，丢掉 nginx 剥掉的前缀。
		w.Header().Set("Location", path.Base(h.mount)+"/")
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	rel, ok := strings.CutPrefix(p, h.mount+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rel == "" || rel == "index.html" {
		h.serveIndex(w, r)
		return
	}
	if !servable(rel) {
		http.NotFound(w, r)
		return
	}
	h.serveFile(w, r, rel)
}

func (h *handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if h.index == nil {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", contentTypes[".html"])
	hd.Set("Content-Security-Policy", indexCSP)
	hd.Set("Cache-Control", indexCache)
	// ServeContent 按这里的 ETag 处理 If-None-Match，命中即 304
	hd.Set("ETag", h.etag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, rel string) {
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
	if strings.HasPrefix(rel, "assets/") {
		hd.Set("Cache-Control", assetCache)
	} else {
		hd.Set("Cache-Control", indexCache)
	}
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
